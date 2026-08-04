package acpruntime

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

const (
	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"
)

type ElicitationRequest struct {
	ID              string
	ToolCallID      string
	Mode            string
	Message         string
	URL             string
	ElicitationID   string
	RequestedSchema any
}

type ElicitationResolution struct {
	ID      string
	Action  string
	Content map[string]any
}

func (c *client) UnstableCreateElicitation(
	ctx context.Context,
	params acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	request, meta, err := newElicitationRequest(params)
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	handler := c.elicitationHandler(meta)
	if handler == nil {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	if params.Url != nil {
		c.mu.Lock()
		c.elicitations[params.Url.ElicitationId] = pendingElicitation{
			requestID: request.ID,
			handler:   handler,
		}
		c.mu.Unlock()
	}

	resolution, err := handler.RequestElicitation(ctx, request)
	if err != nil {
		c.forgetElicitation(params)
		return acp.UnstableCreateElicitationResponse{}, err
	}
	if resolution.ID != request.ID {
		c.forgetElicitation(params)
		return acp.UnstableCreateElicitationResponse{}, fmt.Errorf(
			"ACP elicitation response does not match the request",
		)
	}
	switch resolution.Action {
	case ElicitationActionAccept:
		response := acp.NewUnstableCreateElicitationResponseAccept()
		response.Accept.Content = resolution.Content
		return response, nil
	case ElicitationActionDecline:
		c.forgetElicitation(params)
		return acp.NewUnstableCreateElicitationResponseDecline(), nil
	case ElicitationActionCancel:
		c.forgetElicitation(params)
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	default:
		c.forgetElicitation(params)
		return acp.UnstableCreateElicitationResponse{}, fmt.Errorf(
			"invalid ACP elicitation action %q",
			resolution.Action,
		)
	}
}

func (c *client) UnstableCompleteElicitation(
	ctx context.Context,
	params acp.UnstableCompleteElicitationNotification,
) error {
	c.mu.Lock()
	pending, ok := c.elicitations[params.ElicitationId]
	delete(c.elicitations, params.ElicitationId)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	completionHandler, ok := pending.handler.(ElicitationCompletionHandler)
	if !ok {
		return nil
	}
	return completionHandler.CompleteElicitation(
		ctx,
		pending.requestID,
		string(params.ElicitationId),
	)
}

func (c *client) elicitationHandler(meta map[string]any) ElicitationHandler {
	if sessionID := metadataString(meta, "sessionId"); sessionID != "" {
		c.mu.RLock()
		session := c.sessions[acp.SessionId(sessionID)]
		c.mu.RUnlock()
		handler, _ := session.handler.(ElicitationHandler)
		return handler
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	var matched ElicitationHandler
	for _, session := range c.sessions {
		handler, ok := session.handler.(ElicitationHandler)
		if !ok {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = handler
	}
	return matched
}

func (c *client) forgetElicitation(params acp.UnstableCreateElicitationRequest) {
	if params.Url == nil {
		return
	}
	c.mu.Lock()
	delete(c.elicitations, params.Url.ElicitationId)
	c.mu.Unlock()
}

func newElicitationRequest(
	params acp.UnstableCreateElicitationRequest,
) (ElicitationRequest, map[string]any, error) {
	request := ElicitationRequest{ID: uuid.NewString()}
	var meta map[string]any
	switch {
	case params.Form != nil:
		request.Mode = "form"
		request.Message = strings.TrimSpace(params.Form.Message)
		request.RequestedSchema = params.Form.RequestedSchema
		meta = params.Form.Meta
	case params.Url != nil:
		request.Mode = "url"
		request.Message = strings.TrimSpace(params.Url.Message)
		request.ElicitationID = string(params.Url.ElicitationId)
		meta = params.Url.Meta
		requestedURL, err := safeElicitationURL(params.Url.Url)
		if err != nil {
			return ElicitationRequest{}, nil, err
		}
		request.URL = requestedURL
	default:
		return ElicitationRequest{}, nil, fmt.Errorf("ACP elicitation mode is missing")
	}
	request.ToolCallID = metadataString(meta, "toolCallId")
	return request, meta, nil
}

func safeElicitationURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		return "", fmt.Errorf("ACP elicitation URL must be an absolute HTTP or HTTPS URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("ACP elicitation URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("ACP elicitation URL must not contain credentials")
	}
	return parsed.String(), nil
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}
