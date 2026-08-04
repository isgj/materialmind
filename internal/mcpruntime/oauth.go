package mcpruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"materialmind/internal/credentialstore"
	"materialmind/internal/store"
)

var ErrOAuthRequired = errors.New("MCP server requires OAuth authorization in Settings")

const (
	OAuthStateNotApplicable = "not_applicable"
	OAuthStateDisconnected  = "disconnected"
	OAuthStatePending       = "pending"
	OAuthStateConnected     = "connected"
	OAuthStateError         = "error"
)

type OAuthStart struct {
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	State            string `json:"state,omitempty"`
	Connected        bool   `json:"connected"`
}

type OAuthStatus struct {
	State             string `json:"state"`
	Error             string `json:"error,omitempty"`
	CredentialStorage string `json:"credentialStorage"`
}

type oauthCallback struct {
	code  string
	state string
	err   string
}

type oauthFlow struct {
	serverID         string
	state            string
	authorizationURL string
	callback         chan oauthCallback
	ready            chan struct{}
	done             chan struct{}
	readyOnce        sync.Once
	doneOnce         sync.Once
	cancelled        bool
}

type oauthCoordinator struct {
	ctx         context.Context
	store       *store.Store
	credentials credentialstore.Store

	mu             sync.Mutex
	assigned       map[string]*oauthFlow
	byState        map[string]*oauthFlow
	statusByServer map[string]OAuthStatus
}

func newOAuthCoordinator(
	ctx context.Context,
	dataStore *store.Store,
	credentials credentialstore.Store,
) *oauthCoordinator {
	return &oauthCoordinator{
		ctx:            ctx,
		store:          dataStore,
		credentials:    credentials,
		assigned:       make(map[string]*oauthFlow),
		byState:        make(map[string]*oauthFlow),
		statusByServer: make(map[string]OAuthStatus),
	}
}

func (c *oauthCoordinator) begin(serverID string) (*oauthFlow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.assigned[serverID]; existing != nil {
		return nil, fmt.Errorf("%w: OAuth authorization is already pending", store.ErrConflict)
	}
	flow := &oauthFlow{
		serverID: serverID,
		callback: make(chan oauthCallback, 1),
		ready:    make(chan struct{}),
		done:     make(chan struct{}),
	}
	c.assigned[serverID] = flow
	c.statusByServer[serverID] = OAuthStatus{
		State:             OAuthStatePending,
		CredentialStorage: c.credentials.Backend(),
	}
	return flow, nil
}

func (c *oauthCoordinator) assign(flow *oauthFlow) {
	if flow == nil {
		return
	}
	c.mu.Lock()
	c.assigned[flow.serverID] = flow
	c.mu.Unlock()
}

func (c *oauthCoordinator) authorizationFlow(serverID, state, authorizationURL string) (*oauthFlow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flow := c.assigned[serverID]
	if flow == nil {
		return nil, ErrOAuthRequired
	}
	if flow.state != "" {
		return nil, fmt.Errorf("%w: OAuth authorization is already in progress", store.ErrConflict)
	}
	flow.state = state
	flow.authorizationURL = authorizationURL
	c.byState[state] = flow
	flow.readyOnce.Do(func() { close(flow.ready) })
	return flow, nil
}

func (c *oauthCoordinator) complete(state, code, oauthError string) error {
	c.mu.Lock()
	flow := c.byState[state]
	if flow != nil {
		delete(c.byState, state)
	}
	c.mu.Unlock()
	if flow == nil {
		return fmt.Errorf("%w: OAuth callback state is invalid or expired", store.ErrInvalidInput)
	}
	select {
	case flow.callback <- oauthCallback{code: code, state: state, err: oauthError}:
		return nil
	case <-flow.done:
		return fmt.Errorf("%w: OAuth authorization is no longer pending", store.ErrConflict)
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c *oauthCoordinator) finish(flow *oauthFlow, err error) {
	if flow == nil {
		return
	}
	c.mu.Lock()
	if c.assigned[flow.serverID] == flow {
		delete(c.assigned, flow.serverID)
	}
	if flow.state != "" && c.byState[flow.state] == flow {
		delete(c.byState, flow.state)
	}
	if flow.cancelled {
		delete(c.statusByServer, flow.serverID)
	} else {
		status := OAuthStatus{
			State:             OAuthStateConnected,
			CredentialStorage: c.credentials.Backend(),
		}
		if err != nil {
			status.State = OAuthStateError
			status.Error = err.Error()
		}
		c.statusByServer[flow.serverID] = status
	}
	c.mu.Unlock()
	flow.doneOnce.Do(func() { close(flow.done) })
}

func (c *oauthCoordinator) status(serverID string) OAuthStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status, ok := c.statusByServer[serverID]
	if !ok {
		status = OAuthStatus{State: OAuthStateDisconnected}
	}
	status.CredentialStorage = c.credentials.Backend()
	return status
}

func (c *oauthCoordinator) statusWithCredentials(serverID string) OAuthStatus {
	status := c.status(serverID)
	if status.State == OAuthStatePending || status.State == OAuthStateConnected {
		return status
	}
	_, metadataErr := c.store.GetMCPOAuthMetadata(context.Background(), serverID)
	_, credentialErr := c.credentials.Get(credentialstore.RefreshTokenKey(serverID))
	if metadataErr == nil && credentialErr == nil {
		status.State = OAuthStateConnected
		status.Error = ""
	}
	return status
}

func (c *oauthCoordinator) connected(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusByServer[serverID] = OAuthStatus{
		State:             OAuthStateConnected,
		CredentialStorage: c.credentials.Backend(),
	}
}

func (c *oauthCoordinator) disconnected(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if flow := c.assigned[serverID]; flow != nil {
		flow.cancelled = true
		delete(c.assigned, serverID)
		if flow.state != "" && c.byState[flow.state] == flow {
			delete(c.byState, flow.state)
		}
	}
	delete(c.statusByServer, serverID)
}

type oauthHandler struct {
	server      store.MCPServer
	store       *store.Store
	credentials credentialstore.Store
	coordinator *oauthCoordinator
	callbackURL string
	httpClient  *http.Client

	mu          sync.Mutex
	tokenSource oauth2.TokenSource
}

func newOAuthHandler(
	server store.MCPServer,
	dataStore *store.Store,
	credentials credentialstore.Store,
	coordinator *oauthCoordinator,
	callbackURL string,
) *oauthHandler {
	return &oauthHandler{
		server:      server,
		store:       dataStore,
		credentials: credentials,
		coordinator: coordinator,
		callbackURL: callbackURL,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *oauthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokenSource != nil {
		return h.tokenSource, nil
	}
	metadata, err := h.store.GetMCPOAuthMetadata(ctx, h.server.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	refreshToken, err := h.credentials.Get(credentialstore.RefreshTokenKey(h.server.ID))
	if errors.Is(err, credentialstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	config, err := h.oauthConfig(ctx, metadata)
	if err != nil {
		return nil, err
	}
	expired := &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	}
	source := &persistingTokenSource{
		base:        config.TokenSource(oauthHTTPContext(ctx, h.httpClient), expired),
		serverID:    h.server.ID,
		credentials: h.credentials,
		coordinator: h.coordinator,
	}
	token, err := source.Token()
	if err != nil {
		_ = h.credentials.Delete(credentialstore.RefreshTokenKey(h.server.ID))
		return nil, nil
	}
	// The initial refresh belongs to the connection attempt, but the cached source
	// outlives that caller and must remain usable by later session connections.
	source.base = config.TokenSource(oauthHTTPContext(h.coordinator.ctx, h.httpClient), token)
	h.tokenSource = source
	return h.tokenSource, nil
}

func (h *oauthHandler) Authorize(
	ctx context.Context,
	request *http.Request,
	response *http.Response,
) error {
	defer response.Body.Close()
	defer io.Copy(io.Discard, response.Body)

	challenges, err := oauthex.ParseWWWAuthenticate(
		response.Header.Values("WWW-Authenticate"),
	)
	if err != nil {
		return fmt.Errorf("parse MCP OAuth challenge: %w", err)
	}
	if response.StatusCode == http.StatusForbidden &&
		challengeError(challenges) != "insufficient_scope" {
		return nil
	}
	protectedResource, err := discoverProtectedResource(
		ctx,
		request.URL.String(),
		challenges,
		h.httpClient,
	)
	if err != nil {
		return err
	}
	authorizationServer, err := discoverAuthorizationServer(
		ctx,
		protectedResource.AuthorizationServers[0],
		h.httpClient,
	)
	if err != nil {
		return fmt.Errorf("discover MCP authorization server: %w", err)
	}
	if authorizationServer == nil {
		root := strings.TrimRight(protectedResource.AuthorizationServers[0], "/")
		authorizationServer = &oauthex.AuthServerMeta{
			Issuer:                root,
			AuthorizationEndpoint: root + "/authorize",
			TokenEndpoint:         root + "/token",
			RegistrationEndpoint:  root + "/register",
		}
	}
	scopes := requestedOAuthScopes(
		h.server.OAuthScopes,
		challenges,
		protectedResource.ScopesSupported,
	)
	metadata, secret, err := h.resolveOAuthClient(
		ctx,
		protectedResource,
		authorizationServer,
		scopes,
	)
	if err != nil {
		return err
	}
	config := oauthConfig(metadata, secret, h.callbackURL)
	verifier := oauth2.GenerateVerifier()
	state := rand.Text()
	authorizationURL := config.AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", protectedResource.Resource),
		oauth2.AccessTypeOffline,
	)
	flow, err := h.coordinator.authorizationFlow(
		h.server.ID,
		state,
		authorizationURL,
	)
	if err != nil {
		return err
	}

	var callback oauthCallback
	select {
	case callback = <-flow.callback:
	case <-ctx.Done():
		return ctx.Err()
	case <-h.coordinator.ctx.Done():
		return h.coordinator.ctx.Err()
	}
	if callback.err != "" {
		return fmt.Errorf("OAuth authorization was rejected: %s", callback.err)
	}
	if callback.state != state || strings.TrimSpace(callback.code) == "" {
		return errors.New("OAuth callback state or authorization code is invalid")
	}
	token, err := config.Exchange(
		oauthHTTPContext(ctx, h.httpClient),
		callback.code,
		oauth2.VerifierOption(verifier),
		oauth2.SetAuthURLParam("resource", protectedResource.Resource),
	)
	if err != nil {
		return fmt.Errorf("exchange MCP OAuth authorization code: %w", err)
	}
	source := &persistingTokenSource{
		base:        config.TokenSource(oauthHTTPContext(h.coordinator.ctx, h.httpClient), token),
		serverID:    h.server.ID,
		credentials: h.credentials,
		coordinator: h.coordinator,
	}
	if token.RefreshToken != "" {
		if err := h.credentials.Set(
			credentialstore.RefreshTokenKey(h.server.ID),
			token.RefreshToken,
		); err != nil {
			return err
		}
	}
	h.mu.Lock()
	h.tokenSource = source
	h.mu.Unlock()
	h.coordinator.connected(h.server.ID)
	return nil
}

func (h *oauthHandler) resolveOAuthClient(
	ctx context.Context,
	resource *oauthex.ProtectedResourceMetadata,
	authorizationServer *oauthex.AuthServerMeta,
	scopes []string,
) (store.MCPOAuthMetadata, string, error) {
	if existing, err := h.store.GetMCPOAuthMetadata(ctx, h.server.ID); err == nil &&
		existing.Resource == resource.Resource &&
		existing.AuthorizationEndpoint == authorizationServer.AuthorizationEndpoint &&
		existing.TokenEndpoint == authorizationServer.TokenEndpoint {
		secret, secretErr := h.clientSecret(existing)
		if secretErr == nil {
			existing.Scopes = scopes
			return existing, secret, nil
		}
	}

	var clientID, secret, authMethod string
	switch h.server.OAuthClientMode {
	case store.MCPOAuthClientPreRegistered:
		clientID = h.server.OAuthClientID
		if h.server.OAuthClientSecretEnvVar != "" {
			secret = strings.TrimSpace(
				getenv(h.server.OAuthClientSecretEnvVar),
			)
			if secret == "" {
				return store.MCPOAuthMetadata{}, "", fmt.Errorf(
					"OAuth client secret environment variable %s is not set",
					h.server.OAuthClientSecretEnvVar,
				)
			}
		}
		authMethod = preferredTokenAuthMethod(
			authorizationServer.TokenEndpointAuthMethodsSupported,
			secret != "",
		)
	case store.MCPOAuthClientDynamic:
		if authorizationServer.RegistrationEndpoint == "" {
			return store.MCPOAuthMetadata{}, "", errors.New(
				"authorization server does not support dynamic client registration; configure a pre-registered OAuth client",
			)
		}
		registration, err := oauthex.RegisterClient(
			ctx,
			authorizationServer.RegistrationEndpoint,
			&oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{h.callbackURL},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				ClientName:              "MaterialMind",
				Scope:                   strings.Join(scopes, " "),
				ApplicationType:         "native",
			},
			h.httpClient,
		)
		if err != nil {
			return store.MCPOAuthMetadata{}, "", fmt.Errorf("register MCP OAuth client: %w", err)
		}
		clientID = registration.ClientID
		secret = registration.ClientSecret
		authMethod = registration.TokenEndpointAuthMethod
		if authMethod == "" {
			authMethod = preferredTokenAuthMethod(
				authorizationServer.TokenEndpointAuthMethodsSupported,
				secret != "",
			)
		}
		if secret != "" {
			if err := h.credentials.Set(
				credentialstore.ClientSecretKey(h.server.ID),
				secret,
			); err != nil {
				return store.MCPOAuthMetadata{}, "", err
			}
		}
	default:
		return store.MCPOAuthMetadata{}, "", fmt.Errorf(
			"unsupported OAuth client mode %q",
			h.server.OAuthClientMode,
		)
	}
	metadata := store.MCPOAuthMetadata{
		MCPServerID:           h.server.ID,
		Resource:              resource.Resource,
		AuthorizationEndpoint: authorizationServer.AuthorizationEndpoint,
		TokenEndpoint:         authorizationServer.TokenEndpoint,
		RegistrationEndpoint:  authorizationServer.RegistrationEndpoint,
		Scopes:                scopes,
		ClientID:              clientID,
		TokenAuthMethod:       authMethod,
	}
	if err := h.store.UpsertMCPOAuthMetadata(ctx, metadata); err != nil {
		return store.MCPOAuthMetadata{}, "", err
	}
	return metadata, secret, nil
}

func (h *oauthHandler) clientSecret(metadata store.MCPOAuthMetadata) (string, error) {
	if h.server.OAuthClientMode == store.MCPOAuthClientPreRegistered {
		if h.server.OAuthClientSecretEnvVar == "" {
			return "", nil
		}
		secret := strings.TrimSpace(getenv(h.server.OAuthClientSecretEnvVar))
		if secret == "" {
			return "", credentialstore.ErrNotFound
		}
		return secret, nil
	}
	secret, err := h.credentials.Get(credentialstore.ClientSecretKey(h.server.ID))
	if errors.Is(err, credentialstore.ErrNotFound) &&
		(metadata.TokenAuthMethod == "" || metadata.TokenAuthMethod == "none") {
		return "", nil
	}
	return secret, err
}

func (h *oauthHandler) oauthConfig(
	ctx context.Context,
	metadata store.MCPOAuthMetadata,
) (*oauth2.Config, error) {
	secret, err := h.clientSecret(metadata)
	if err != nil {
		return nil, err
	}
	return oauthConfig(metadata, secret, h.callbackURL), nil
}

type persistingTokenSource struct {
	mu          sync.Mutex
	base        oauth2.TokenSource
	serverID    string
	credentials credentialstore.Store
	coordinator *oauthCoordinator
	lastRefresh string
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	if token.RefreshToken != "" && token.RefreshToken != s.lastRefresh {
		if err := s.credentials.Set(
			credentialstore.RefreshTokenKey(s.serverID),
			token.RefreshToken,
		); err != nil {
			return nil, err
		}
		s.lastRefresh = token.RefreshToken
	}
	s.coordinator.connected(s.serverID)
	return token, nil
}

func discoverProtectedResource(
	ctx context.Context,
	resourceURL string,
	challenges []oauthex.Challenge,
	client *http.Client,
) (*oauthex.ProtectedResourceMetadata, error) {
	for _, candidate := range protectedResourceMetadataURLs(challenges, resourceURL) {
		metadata, err := oauthex.GetProtectedResourceMetadata(
			ctx,
			candidate.metadataURL,
			candidate.resource,
			client,
		)
		if err != nil || metadata == nil {
			continue
		}
		if len(metadata.AuthorizationServers) == 0 {
			return nil, errors.New("MCP protected resource metadata has no authorization server")
		}
		return metadata, nil
	}
	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("parse MCP resource URL: %w", err)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	root := strings.TrimRight(parsed.String(), "/")
	return &oauthex.ProtectedResourceMetadata{
		Resource:             resourceURL,
		AuthorizationServers: []string{root},
	}, nil
}

func discoverAuthorizationServer(
	ctx context.Context,
	issuerURL string,
	client *http.Client,
) (*oauthex.AuthServerMeta, error) {
	metadata, err := auth.GetAuthServerMetadata(ctx, issuerURL, client)
	if err == nil {
		return metadata, nil
	}
	if !strings.Contains(err.Error(), "does not match issuer URL") {
		return nil, err
	}

	compatible, compatibilityErr := discoverPathQualifiedAuthorizationServer(
		ctx,
		issuerURL,
		client,
	)
	if compatibilityErr != nil {
		return nil, errors.Join(err, compatibilityErr)
	}
	slog.Warn(
		"MCP authorization server advertised a path-qualified discovery URL with a canonical origin issuer",
		"advertised_issuer",
		issuerURL,
		"metadata_issuer",
		compatible.Issuer,
	)
	return compatible, nil
}

func discoverPathQualifiedAuthorizationServer(
	ctx context.Context,
	issuerURL string,
	client *http.Client,
) (*oauthex.AuthServerMeta, error) {
	advertised, err := url.Parse(issuerURL)
	if err != nil ||
		advertised.Scheme == "" ||
		advertised.Host == "" ||
		advertised.Path == "" ||
		advertised.Path == "/" ||
		advertised.RawQuery != "" ||
		advertised.Fragment != "" {
		return nil, fmt.Errorf("authorization server issuer is not a path-qualified URL")
	}
	origin := (&url.URL{Scheme: advertised.Scheme, Host: advertised.Host}).String()
	canonical, err := auth.GetAuthServerMetadata(ctx, origin, client)
	if err != nil {
		return nil, fmt.Errorf("discover canonical authorization server issuer: %w", err)
	}
	if canonical == nil {
		return nil, errors.New("canonical authorization server metadata is unavailable")
	}

	for _, metadataURL := range authorizationServerMetadataURLs(issuerURL) {
		pathMetadata, err := oauthex.GetAuthServerMeta(
			ctx,
			metadataURL,
			canonical.Issuer,
			client,
		)
		if err != nil {
			return nil, fmt.Errorf("validate path-qualified authorization server metadata: %w", err)
		}
		if pathMetadata == nil {
			continue
		}
		if pathMetadata.AuthorizationEndpoint != canonical.AuthorizationEndpoint ||
			pathMetadata.TokenEndpoint != canonical.TokenEndpoint ||
			pathMetadata.JWKSURI != canonical.JWKSURI {
			return nil, errors.New(
				"path-qualified authorization server metadata differs from its canonical issuer",
			)
		}
		compatible := *canonical
		if pathMetadata.RegistrationEndpoint != "" {
			compatible.RegistrationEndpoint = pathMetadata.RegistrationEndpoint
		}
		return &compatible, nil
	}
	return nil, errors.New("path-qualified authorization server metadata is unavailable")
}

func authorizationServerMetadataURLs(issuerURL string) []string {
	issuer, err := url.Parse(issuerURL)
	if err != nil {
		return nil
	}
	path := strings.Trim(issuer.Path, "/")
	if path == "" {
		issuer.Path = "/.well-known/oauth-authorization-server"
		return []string{issuer.String()}
	}
	issuer.Path = "/.well-known/oauth-authorization-server/" + path
	oauthMetadataURL := issuer.String()
	issuer.Path = "/.well-known/openid-configuration/" + path
	oidcInsertedURL := issuer.String()
	issuer.Path = "/" + path + "/.well-known/openid-configuration"
	return []string{oauthMetadataURL, oidcInsertedURL, issuer.String()}
}

type protectedResourceCandidate struct {
	metadataURL string
	resource    string
}

func protectedResourceMetadataURLs(
	challenges []oauthex.Challenge,
	resourceURL string,
) []protectedResourceCandidate {
	result := make([]protectedResourceCandidate, 0, 3)
	for _, challenge := range challenges {
		if metadataURL := challenge.Params["resource_metadata"]; metadataURL != "" {
			result = append(result, protectedResourceCandidate{
				metadataURL: metadataURL,
				resource:    resourceURL,
			})
			break
		}
	}
	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return result
	}
	atPath := *parsed
	atPath.Path = "/.well-known/oauth-protected-resource/" +
		strings.TrimLeft(parsed.Path, "/")
	result = append(result, protectedResourceCandidate{
		metadataURL: atPath.String(),
		resource:    resourceURL,
	})
	atRoot := *parsed
	atRoot.Path = "/.well-known/oauth-protected-resource"
	atRoot.RawPath = ""
	atRoot.RawQuery = ""
	resourceRoot := *parsed
	resourceRoot.Path = ""
	resourceRoot.RawPath = ""
	resourceRoot.RawQuery = ""
	result = append(result, protectedResourceCandidate{
		metadataURL: atRoot.String(),
		resource:    resourceRoot.String(),
	})
	return result
}

func requestedOAuthScopes(
	configured []string,
	challenges []oauthex.Challenge,
	supported []string,
) []string {
	result := slices.Clone(configured)
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" {
			result = append(result, strings.Fields(challenge.Params["scope"])...)
		}
	}
	if len(result) == 0 {
		result = append(result, supported...)
	}
	slices.Sort(result)
	result = slices.Compact(result)
	return result
}

func challengeError(challenges []oauthex.Challenge) string {
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" {
			return challenge.Params["error"]
		}
	}
	return ""
}

func preferredTokenAuthMethod(supported []string, hasSecret bool) string {
	if !hasSecret && slices.Contains(supported, "none") {
		return "none"
	}
	if slices.Contains(supported, "client_secret_post") {
		return "client_secret_post"
	}
	if slices.Contains(supported, "client_secret_basic") {
		return "client_secret_basic"
	}
	if !hasSecret {
		return "none"
	}
	return "client_secret_basic"
}

func oauthConfig(
	metadata store.MCPOAuthMetadata,
	secret, callbackURL string,
) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     metadata.ClientID,
		ClientSecret: secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   metadata.AuthorizationEndpoint,
			TokenURL:  metadata.TokenEndpoint,
			AuthStyle: oauthAuthStyle(metadata.TokenAuthMethod),
		},
		RedirectURL: callbackURL,
		Scopes:      metadata.Scopes,
	}
}

func oauthAuthStyle(method string) oauth2.AuthStyle {
	switch method {
	case "client_secret_basic":
		return oauth2.AuthStyleInHeader
	case "none", "client_secret_post":
		return oauth2.AuthStyleInParams
	default:
		return oauth2.AuthStyleAutoDetect
	}
}

func oauthHTTPContext(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

var getenv = os.Getenv
