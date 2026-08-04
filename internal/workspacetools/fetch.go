package workspacetools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

const (
	maxFetchBytes = 256 * 1024
	fetchTimeout  = 20 * time.Second
	maxRedirects  = 5
)

var defaultFetchClient = newFetchClient()

type FetchURLArgs struct {
	URL string `json:"url" jsonschema:"Absolute public HTTP or HTTPS URL to fetch."`
}

type FetchURLResult struct {
	State       string `json:"state"`
	URL         string `json:"url"`
	FinalURL    string `json:"finalUrl,omitempty"`
	HTTPStatus  int    `json:"httpStatus,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Content     string `json:"content,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type fetchConfirmationPayload struct {
	Kind         string   `json:"kind"`
	URL          string   `json:"url"`
	RequestedURL string   `json:"requestedUrl"`
	ApprovedURLs []string `json:"approvedUrls"`
}

type redirectApprovalRequiredError struct {
	url string
}

func (err *redirectApprovalRequiredError) Error() string {
	return "redirect requires approval: " + err.url
}

func newFetchTool(provided ...toolpolicy.Permission) (tool.Tool, error) {
	permission := configuredPermission(toolpolicy.ToolFetchURL, provided)
	confirmationDescription := "Calls follow the configured URL confirmation policy."
	if permission.ConfirmationMode == toolpolicy.ConfirmationAllow && len(permission.TargetRules) == 0 {
		confirmationDescription = "Valid public URLs may be fetched without user confirmation."
	}
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name:        toolpolicy.ToolFetchURL,
			Description: "Fetch a public HTTP or HTTPS URL and return a bounded text response. " + confirmationDescription,
		},
		func(ctx agent.Context, args FetchURLArgs) (FetchURLResult, error) {
			return fetchURLWithPolicy(permission, ctx, args)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, fetchDeniedResult)
}

func fetchDeniedResult(input map[string]any, confirmation *toolconfirmation.ToolConfirmation) (map[string]any, error) {
	rawURL, ok := input["url"].(string)
	if !ok {
		return nil, fmt.Errorf("fetch URL is required")
	}
	normalizedURL, err := normalizeFetchURL(rawURL)
	if err != nil {
		return nil, err
	}
	if requestedURL := fetchConfirmationURL(confirmation); requestedURL != "" {
		normalizedURL = requestedURL
	}
	return map[string]any{
		"state":  "denied",
		"url":    normalizedURL,
		"reason": approvalReason(confirmation),
	}, nil
}

func fetchURL(ctx agent.Context, args FetchURLArgs) (FetchURLResult, error) {
	return fetchURLWithPolicy(configuredPermission(toolpolicy.ToolFetchURL, nil), ctx, args)
}

func fetchURLWithPolicy(permission toolpolicy.Permission, ctx agent.Context, args FetchURLArgs) (FetchURLResult, error) {
	normalizedURL, err := normalizeFetchURL(args.URL)
	if err != nil {
		return FetchURLResult{}, err
	}

	confirmation := ctx.ToolConfirmation()
	mode, err := toolpolicy.ConfirmationForURL(permission, normalizedURL)
	if err != nil {
		return FetchURLResult{}, err
	}
	if confirmation == nil && mode == toolpolicy.ConfirmationAsk {
		if err := requestFetchConfirmation(ctx, normalizedURL, normalizedURL, nil); err != nil {
			return FetchURLResult{}, err
		}
		return FetchURLResult{State: "approval_required", URL: normalizedURL}, nil
	}

	if confirmation != nil && !confirmation.Confirmed {
		return FetchURLResult{
			State:  "denied",
			URL:    normalizedURL,
			Reason: approvalReason(confirmation),
		}, nil
	}

	approvedURLs, err := approvedFetchURLs(confirmation)
	if err != nil {
		return FetchURLResult{}, err
	}
	return fetchContentWithPolicy(ctx, normalizedURL, defaultFetchClient, permission, approvedURLs)
}

func requestFetchConfirmation(ctx agent.Context, requestedURL, targetURL string, previouslyApproved []string) error {
	approved := append([]string{}, previouslyApproved...)
	if !slices.Contains(approved, targetURL) {
		approved = append(approved, targetURL)
	}
	if err := ctx.RequestConfirmation(
		fmt.Sprintf("Allow the agent to fetch %s?", targetURL),
		fetchConfirmationPayload{
			Kind:         "fetch_url",
			URL:          targetURL,
			RequestedURL: requestedURL,
			ApprovedURLs: approved,
		},
	); err != nil {
		return fmt.Errorf("request fetch approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func approvedFetchURLs(confirmation *toolconfirmation.ToolConfirmation) ([]string, error) {
	if confirmation == nil || confirmation.Payload == nil {
		return []string{}, nil
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode fetch approval payload: %w", err)
	}
	var payload fetchConfirmationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode fetch approval payload: %w", err)
	}
	result := make([]string, 0, len(payload.ApprovedURLs))
	for _, approvedURL := range payload.ApprovedURLs {
		normalized, err := normalizeFetchURL(approvedURL)
		if err != nil {
			return nil, fmt.Errorf("validate approved fetch URL: %w", err)
		}
		if !slices.Contains(result, normalized) {
			result = append(result, normalized)
		}
	}
	return result, nil
}

func fetchConfirmationURL(confirmation *toolconfirmation.ToolConfirmation) string {
	if confirmation == nil || confirmation.Payload == nil {
		return ""
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return ""
	}
	var payload fetchConfirmationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return ""
	}
	normalized, err := normalizeFetchURL(payload.URL)
	if err != nil {
		return ""
	}
	return normalized
}

func fetchContentWithPolicy(ctx agent.Context, rawURL string, client *http.Client, permission toolpolicy.Permission, approvedURLs []string) (FetchURLResult, error) {
	requestClient := *client
	requestClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("too many redirects")
		}
		normalized, err := normalizeFetchURL(request.URL.String())
		if err != nil {
			return err
		}
		mode, err := toolpolicy.ConfirmationForURL(permission, normalized)
		if err != nil {
			return err
		}
		if mode == toolpolicy.ConfirmationAllow || slices.Contains(approvedURLs, normalized) {
			return nil
		}
		return &redirectApprovalRequiredError{url: normalized}
	}
	result, err := fetchContent(ctx, rawURL, &requestClient)
	var approvalError *redirectApprovalRequiredError
	if errors.As(err, &approvalError) {
		if requestErr := requestFetchConfirmation(ctx, rawURL, approvalError.url, approvedURLs); requestErr != nil {
			return FetchURLResult{}, requestErr
		}
		return FetchURLResult{State: "approval_required", URL: approvalError.url}, nil
	}
	return result, err
}

func fetchContent(ctx context.Context, rawURL string, client *http.Client) (FetchURLResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return FetchURLResult{}, fmt.Errorf("create fetch request: %w", err)
	}
	request.Header.Set("Accept", "text/*, application/json, application/*+json, application/xml, application/*+xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "MaterialMind/1.0")

	response, err := client.Do(request)
	if err != nil {
		return FetchURLResult{}, fmt.Errorf("fetch %q: %w", rawURL, err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, maxFetchBytes+1))
	if err != nil {
		return FetchURLResult{}, fmt.Errorf("read response from %q: %w", rawURL, err)
	}
	truncated := len(data) > maxFetchBytes
	if truncated {
		data = data[:maxFetchBytes]
	}
	contentType := response.Header.Get("Content-Type")
	if !isTextContent(contentType, data) {
		return FetchURLResult{}, fmt.Errorf("fetch %q returned unsupported content type %q", rawURL, contentType)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return FetchURLResult{}, fmt.Errorf("fetch %q returned non-UTF-8 or binary content", rawURL)
	}

	return FetchURLResult{
		State:       "fetched",
		URL:         rawURL,
		FinalURL:    response.Request.URL.String(),
		HTTPStatus:  response.StatusCode,
		ContentType: contentType,
		Content:     string(data),
		Truncated:   truncated,
	}, nil
}

func normalizeFetchURL(rawURL string) (string, error) {
	normalized, err := toolpolicy.NormalizeURLTarget(toolpolicy.TargetExactURL, rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse normalized URL: %w", err)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("URL host is required")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return "", fmt.Errorf("URL host must be public")
	}
	if address, err := netip.ParseAddr(hostname); err == nil && !isPublicAddress(address) {
		return "", fmt.Errorf("URL host must be public")
	}
	return parsed.String(), nil
}

func newFetchClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeDialContext
	return &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			_, err := normalizeFetchURL(request.URL.String())
			return err
		},
	}
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse destination address: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("destination %q has no IP addresses", host)
	}
	for _, resolved := range addresses {
		if !isPublicAddress(resolved.Unmap()) {
			return nil, fmt.Errorf("destination %q resolves to a non-public address", host)
		}
	}

	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, resolved := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("connect to destination %q: %w", host, lastErr)
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	carrierGradeNAT := netip.MustParsePrefix("100.64.0.0/10")
	return !carrierGradeNAT.Contains(address)
}

func isTextContent(contentType string, data []byte) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(data))
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" || mediaType == "application/x-javascript" ||
		mediaType == "application/x-www-form-urlencoded"
}
