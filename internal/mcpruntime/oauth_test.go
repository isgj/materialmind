package mcpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/credentialstore"
	"materialmind/internal/store"
)

func TestManagerCompletesOAuthAndPersistsRefreshToken(t *testing.T) {
	var baseURL string
	var authenticatedRequests atomic.Int64
	protocolServer := mcp.NewServer(
		&mcp.Implementation{Name: "oauth-mcp", Version: "1.0.0"},
		nil,
	)
	protocolServer.AddTool(
		&mcp.Tool{
			Name:        "lookup",
			Description: "Looks up context",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		func(
			context.Context,
			*mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "found"}},
			}, nil
		},
	)
	protocolHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return protocolServer },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.URL.Path == "/mcp":
			if request.Header.Get("Authorization") != "Bearer access-token" {
				writer.Header().Set(
					"WWW-Authenticate",
					`Bearer resource_metadata="`+
						baseURL+
						`/.well-known/oauth-protected-resource/mcp", scope="tools.read"`,
				)
				http.Error(writer, "authorization required", http.StatusUnauthorized)
				return
			}
			authenticatedRequests.Add(1)
			protocolHandler.ServeHTTP(writer, request)
		case strings.HasPrefix(request.URL.Path, "/.well-known/oauth-protected-resource"):
			writeTestJSON(writer, map[string]any{
				"resource":              baseURL + "/mcp",
				"authorization_servers": []string{baseURL},
				"scopes_supported":      []string{"tools.read"},
			})
		case strings.Contains(request.URL.Path, "oauth-authorization-server") ||
			strings.Contains(request.URL.Path, "openid-configuration"):
			writeTestJSON(writer, map[string]any{
				"issuer":                                baseURL,
				"authorization_endpoint":                baseURL + "/authorize",
				"token_endpoint":                        baseURL + "/token",
				"registration_endpoint":                 baseURL + "/register",
				"response_types_supported":              []string{"code"},
				"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
				"code_challenge_methods_supported":      []string{"S256"},
				"token_endpoint_auth_methods_supported": []string{"none"},
			})
		case request.URL.Path == "/token":
			if err := request.ParseForm(); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if request.Form.Get("grant_type") != "authorization_code" ||
				request.Form.Get("code") != "valid-code" ||
				request.Form.Get("resource") != baseURL+"/mcp" {
				http.Error(writer, "invalid token request", http.StatusBadRequest)
				return
			}
			writeTestJSON(writer, map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"scope":         "tools.read",
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	baseURL = upstream.URL
	t.Cleanup(upstream.Close)

	dataStore := openTestStore(t)
	server, err := dataStore.CreateMCPServer(context.Background(), store.MCPServer{
		Name:            "OAuth context",
		Transport:       store.MCPTransportHTTP,
		URL:             baseURL + "/mcp",
		AuthType:        store.MCPAuthOAuth,
		OAuthClientMode: store.MCPOAuthClientPreRegistered,
		OAuthClientID:   "materialmind-test",
		OAuthScopes:     []string{"tools.read"},
	})
	if err != nil {
		t.Fatalf("CreateMCPServer() error = %v", err)
	}
	credentials := credentialstore.NewMemory()
	manager := New(dataStore, Options{
		Credentials: credentials,
		CallbackURL: "http://127.0.0.1:8080/api/mcp-oauth/callback",
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	start, err := manager.StartOAuth(context.Background(), server.ID)
	if err != nil {
		t.Fatalf("StartOAuth() error = %v", err)
	}
	authorizationURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	if authorizationURL.Path != "/authorize" ||
		authorizationURL.Query().Get("state") != start.State ||
		authorizationURL.Query().Get("code_challenge_method") != "S256" ||
		authorizationURL.Query().Get("resource") != baseURL+"/mcp" {
		t.Fatalf("StartOAuth() = %#v, URL = %s", start, authorizationURL)
	}
	if err := manager.CompleteOAuth(start.State, "valid-code", ""); err != nil {
		t.Fatalf("CompleteOAuth() error = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, statusErr := manager.OAuthStatus(context.Background(), server.ID)
		if statusErr != nil {
			t.Fatalf("OAuthStatus() error = %v", statusErr)
		}
		if status.State == OAuthStateConnected {
			break
		}
		if status.State == OAuthStateError {
			t.Fatalf("OAuthStatus() = %#v", status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("OAuthStatus() remained %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	refreshToken, err := credentials.Get(credentialstore.RefreshTokenKey(server.ID))
	if err != nil || refreshToken != "refresh-token" {
		t.Fatalf("stored refresh token = %q, %v", refreshToken, err)
	}
	catalog, err := manager.ListServerTools(context.Background(), server)
	if err != nil {
		t.Fatalf("ListServerTools() error = %v", err)
	}
	tools := catalog.Tools
	if len(tools) != 1 || tools[0].Name != "lookup" || authenticatedRequests.Load() == 0 {
		t.Fatalf("ListServerTools() = %#v, authenticated requests = %d", tools, authenticatedRequests.Load())
	}

	if err := manager.DisconnectOAuth(context.Background(), server.ID); err != nil {
		t.Fatalf("DisconnectOAuth() error = %v", err)
	}
	status, err := manager.OAuthStatus(context.Background(), server.ID)
	if err != nil || status.State != OAuthStateDisconnected {
		t.Fatalf("OAuthStatus() after disconnect = %#v, %v", status, err)
	}
	if _, err := credentials.Get(credentialstore.RefreshTokenKey(server.ID)); !errors.Is(
		err,
		credentialstore.ErrNotFound,
	) {
		t.Fatalf("refresh token after disconnect error = %v", err)
	}
}

func TestRestoredOAuthTokenSourceSurvivesConnectionContextCancellation(t *testing.T) {
	var tokenRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if err := request.ParseForm(); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Form.Get("grant_type") != "refresh_token" ||
			request.Form.Get("refresh_token") != "refresh-token" {
			http.Error(writer, "invalid refresh request", http.StatusBadRequest)
			return
		}
		tokenRequests.Add(1)
		writeTestJSON(writer, map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"token_type":    "Bearer",
			"expires_in":    1,
		})
	}))
	t.Cleanup(upstream.Close)

	dataStore := openTestStore(t)
	server, err := dataStore.CreateMCPServer(context.Background(), store.MCPServer{
		Name:            "Restored OAuth context",
		Transport:       store.MCPTransportHTTP,
		URL:             upstream.URL + "/mcp",
		AuthType:        store.MCPAuthOAuth,
		OAuthClientMode: store.MCPOAuthClientPreRegistered,
		OAuthClientID:   "materialmind-test",
	})
	if err != nil {
		t.Fatalf("CreateMCPServer() error = %v", err)
	}
	if err := dataStore.UpsertMCPOAuthMetadata(context.Background(), store.MCPOAuthMetadata{
		MCPServerID:           server.ID,
		Resource:              server.URL,
		AuthorizationEndpoint: upstream.URL + "/authorize",
		TokenEndpoint:         upstream.URL + "/token",
		ClientID:              server.OAuthClientID,
		TokenAuthMethod:       "none",
	}); err != nil {
		t.Fatalf("UpsertMCPOAuthMetadata() error = %v", err)
	}
	credentials := credentialstore.NewMemory()
	if err := credentials.Set(
		credentialstore.RefreshTokenKey(server.ID),
		"refresh-token",
	); err != nil {
		t.Fatalf("store refresh token: %v", err)
	}
	managerContext, stopManager := context.WithCancel(context.Background())
	t.Cleanup(stopManager)
	handler := newOAuthHandler(
		server,
		dataStore,
		credentials,
		newOAuthCoordinator(managerContext, dataStore, credentials),
		"http://127.0.0.1:8080/api/mcp-oauth/callback",
	)

	connectionContext, cancelConnection := context.WithCancel(context.Background())
	source, err := handler.TokenSource(connectionContext)
	if err != nil {
		t.Fatalf("TokenSource() error = %v", err)
	}
	if source == nil {
		t.Fatal("TokenSource() returned nil")
	}
	cancelConnection()
	if _, err := source.Token(); err != nil {
		t.Fatalf("Token() after connection cancellation error = %v", err)
	}
	if tokenRequests.Load() < 2 {
		t.Fatalf("token requests = %d, want at least two refreshes", tokenRequests.Load())
	}
}

func TestOAuthDisconnectKeepsCancelledFlowDisconnected(t *testing.T) {
	credentials := credentialstore.NewMemory()
	coordinator := newOAuthCoordinator(
		context.Background(),
		openTestStore(t),
		credentials,
	)
	flow, err := coordinator.begin("server-1")
	if err != nil {
		t.Fatalf("begin() error = %v", err)
	}
	if _, err := coordinator.authorizationFlow(
		"server-1",
		"state-1",
		"https://authorization.example.test",
	); err != nil {
		t.Fatalf("authorizationFlow() error = %v", err)
	}
	coordinator.disconnected("server-1")
	coordinator.finish(flow, errors.New("connection cancelled"))

	status := coordinator.status("server-1")
	if status.State != OAuthStateDisconnected {
		t.Fatalf("status() = %#v, want disconnected", status)
	}
	if err := coordinator.complete("state-1", "late-code", ""); !errors.Is(
		err,
		store.ErrInvalidInput,
	) {
		t.Fatalf("complete() late callback error = %v, want invalid input", err)
	}
}

func TestDiscoverAuthorizationServerSupportsCanonicalOriginIssuer(t *testing.T) {
	var baseURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		metadata := map[string]any{
			"issuer":                           baseURL,
			"authorization_endpoint":           baseURL + "/authorize",
			"token_endpoint":                   baseURL + "/token",
			"jwks_uri":                         baseURL + "/jwks",
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
		}
		switch request.URL.Path {
		case "/.well-known/oauth-authorization-server":
			writeTestJSON(writer, metadata)
		case "/.well-known/oauth-authorization-server/tenant":
			metadata["registration_endpoint"] = baseURL + "/tenant/register"
			writeTestJSON(writer, metadata)
		default:
			http.NotFound(writer, request)
		}
	}))
	baseURL = upstream.URL
	t.Cleanup(upstream.Close)

	metadata, err := discoverAuthorizationServer(
		context.Background(),
		baseURL+"/tenant",
		upstream.Client(),
	)
	if err != nil {
		t.Fatalf("discoverAuthorizationServer() error = %v", err)
	}
	if metadata.Issuer != baseURL ||
		metadata.AuthorizationEndpoint != baseURL+"/authorize" ||
		metadata.TokenEndpoint != baseURL+"/token" ||
		metadata.RegistrationEndpoint != baseURL+"/tenant/register" {
		t.Fatalf("discoverAuthorizationServer() = %#v", metadata)
	}
}

func TestDiscoverAuthorizationServerRejectsDivergentPathMetadata(t *testing.T) {
	var baseURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		tokenEndpoint := baseURL + "/token"
		if request.URL.Path == "/.well-known/oauth-authorization-server/tenant" {
			tokenEndpoint = baseURL + "/tenant/token"
		}
		if request.URL.Path != "/.well-known/oauth-authorization-server" &&
			request.URL.Path != "/.well-known/oauth-authorization-server/tenant" {
			http.NotFound(writer, request)
			return
		}
		writeTestJSON(writer, map[string]any{
			"issuer":                           baseURL,
			"authorization_endpoint":           baseURL + "/authorize",
			"token_endpoint":                   tokenEndpoint,
			"jwks_uri":                         baseURL + "/jwks",
			"registration_endpoint":            baseURL + "/tenant/register",
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	}))
	baseURL = upstream.URL
	t.Cleanup(upstream.Close)

	_, err := discoverAuthorizationServer(
		context.Background(),
		baseURL+"/tenant",
		upstream.Client(),
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"path-qualified authorization server metadata differs from its canonical issuer",
		) {
		t.Fatalf("discoverAuthorizationServer() error = %v", err)
	}
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
