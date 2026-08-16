package mcpruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/credentialstore"
	"materialmind/internal/store"
)

const (
	completedCallEventRetention = 30 * time.Second
	defaultToolCallTimeout      = 2 * time.Minute
	modernProtocolVersion       = "2026-07-28"
)

type Options struct {
	Credentials     credentialstore.Store
	CallbackURL     string
	Events          EventSink
	Elicitation     ElicitationHandler
	ToolCallTimeout time.Duration
}

type ToolDefinition struct {
	Name             string `json:"name"`
	OriginalName     string `json:"originalName"`
	Title            string `json:"title,omitempty"`
	Description      string `json:"description,omitempty"`
	ServerID         string `json:"serverId"`
	ServerName       string `json:"serverName"`
	InputSchema      any    `json:"inputSchema"`
	OutputSchema     any    `json:"outputSchema,omitempty"`
	ConfirmationMode string `json:"confirmationMode"`
	UIResourceURI    string `json:"uiResourceUri,omitempty"`

	sessionID     string
	connectionKey string
	session       *mcp.ClientSession
}

type ToolSummary struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type ToolCatalog struct {
	ProtocolVersion   string                    `json:"protocolVersion"`
	ServerName        string                    `json:"serverName,omitempty"`
	ServerVersion     string                    `json:"serverVersion,omitempty"`
	Tools             []ToolSummary             `json:"tools"`
	Prompts           []PromptSummary           `json:"prompts"`
	Resources         []ResourceSummary         `json:"resources"`
	ResourceTemplates []ResourceTemplateSummary `json:"resourceTemplates"`
	Extensions        map[string]any            `json:"extensions,omitempty"`
}

type connection struct {
	key         string
	serverID    string
	fingerprint string
	session     *mcp.ClientSession
	cancel      context.CancelFunc

	toolsMu     sync.RWMutex
	tools       []*mcp.Tool
	toolsLoaded bool
}

type connectionAttempt struct {
	key       string
	serverID  string
	done      chan struct{}
	cancel    context.CancelFunc
	cancelled bool
}

type transportControl struct {
	mu         sync.Mutex
	connection mcp.Connection
	closers    []func()
	closed     bool
}

type controlledTransport struct {
	base    mcp.Transport
	control *transportControl
}

type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	store           *store.Store
	credentials     credentialstore.Store
	callbackURL     string
	events          EventSink
	elicitation     ElicitationHandler
	oauth           *oauthCoordinator
	toolCallTimeout time.Duration

	mu            sync.Mutex
	connections   map[string]*connection
	connecting    map[string]*connectionAttempt
	oauthHandlers map[string]*oauthHandler
	activeCalls   map[string]*activeToolCall
	callsByToken  map[string]*activeToolCall
	nextCallToken atomic.Uint64
	stopping      bool
}

type activeToolCall struct {
	key           string
	token         string
	sessionID     string
	toolCallID    string
	serverID      string
	serverName    string
	toolName      string
	toolTitle     string
	connectionKey string
	timeout       *callInactivityTimeout
	cancel        context.CancelFunc
	userStopped   bool
}

type CallOptions struct {
	ToolCallID string
}

func New(dataStore *store.Store, options Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	credentials := options.Credentials
	if credentials == nil {
		credentials = credentialstore.NewMemory()
	}
	toolCallTimeout := options.ToolCallTimeout
	if toolCallTimeout <= 0 {
		toolCallTimeout = defaultToolCallTimeout
	}
	manager := &Manager{
		ctx:             ctx,
		cancel:          cancel,
		store:           dataStore,
		credentials:     credentials,
		callbackURL:     options.CallbackURL,
		events:          options.Events,
		elicitation:     options.Elicitation,
		toolCallTimeout: toolCallTimeout,
		connections:     make(map[string]*connection),
		connecting:      make(map[string]*connectionAttempt),
		oauthHandlers:   make(map[string]*oauthHandler),
		activeCalls:     make(map[string]*activeToolCall),
		callsByToken:    make(map[string]*activeToolCall),
	}
	manager.oauth = newOAuthCoordinator(ctx, dataStore, credentials)
	return manager
}

func (m *Manager) SessionTools(
	ctx context.Context,
	sessionID, workingDirectory string,
	servers []store.SessionMCPServer,
) ([]ToolDefinition, error) {
	result := make([]ToolDefinition, 0)
	seenNames := make(map[string]struct{})
	for _, configured := range servers {
		key := "session:" + sessionID + ":" + configured.ID
		currentConnection, err := m.connection(
			ctx,
			key,
			workingDirectory,
			configured.MCPServer,
			nil,
		)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			m.reportUnavailable(ctx, sessionID, configured.MCPServer, "connect", err)
			continue
		}
		tools, err := currentConnection.listTools(ctx, false)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			m.closeMatching(func(current *connection) bool {
				return current.key == key
			})
			m.reportUnavailable(ctx, sessionID, configured.MCPServer, "list tools", err)
			continue
		}
		overrides := make(map[string]string, len(configured.ToolPermissions))
		for _, permission := range configured.ToolPermissions {
			overrides[permission.ToolName] = permission.ConfirmationMode
		}
		for _, remoteTool := range tools {
			name := namespacedToolName(configured.ID, configured.Name, remoteTool.Name)
			if _, duplicate := seenNames[name]; duplicate {
				return nil, fmt.Errorf(
					"MCP tool name collision for %q from server %q",
					remoteTool.Name,
					configured.Name,
				)
			}
			seenNames[name] = struct{}{}
			confirmationMode := configured.ConfirmationMode
			if override, ok := overrides[remoteTool.Name]; ok {
				confirmationMode = override
			}
			result = append(result, ToolDefinition{
				Name:             name,
				OriginalName:     remoteTool.Name,
				Title:            toolTitle(remoteTool),
				Description:      remoteTool.Description,
				ServerID:         configured.ID,
				ServerName:       configured.Name,
				InputSchema:      remoteTool.InputSchema,
				OutputSchema:     remoteTool.OutputSchema,
				ConfirmationMode: confirmationMode,
				UIResourceURI:    toolUIResourceURI(remoteTool),
				sessionID:        sessionID,
				connectionKey:    currentConnection.key,
				session:          currentConnection.session,
			})
		}
	}
	return result, nil
}

func (m *Manager) reportUnavailable(
	ctx context.Context,
	sessionID string,
	server store.MCPServer,
	operation string,
	err error,
) {
	slog.WarnContext(
		ctx,
		"MCP server unavailable for session",
		"mcp_server_id",
		server.ID,
		"mcp_server_name",
		server.Name,
		"operation",
		operation,
		"error",
		err,
	)
	m.emit(Event{
		Type:       EventUnavailable,
		SessionID:  sessionID,
		ServerID:   server.ID,
		ServerName: server.Name,
		Error:      fmt.Sprintf("%s: %v", operation, err),
	})
}

func (m *Manager) ListServerTools(
	ctx context.Context,
	server store.MCPServer,
) (ToolCatalog, error) {
	connection, err := m.connection(
		ctx,
		"definition:"+server.ID,
		"",
		server,
		nil,
	)
	if err != nil {
		return ToolCatalog{}, err
	}
	tools, err := connection.listTools(ctx, true)
	if err != nil {
		return ToolCatalog{}, err
	}
	result := make([]ToolSummary, 0, len(tools))
	for _, remoteTool := range tools {
		result = append(result, ToolSummary{
			Name:        remoteTool.Name,
			Title:       toolTitle(remoteTool),
			Description: remoteTool.Description,
		})
	}
	catalog := ToolCatalog{
		Tools:             result,
		Prompts:           []PromptSummary{},
		Resources:         []ResourceSummary{},
		ResourceTemplates: []ResourceTemplateSummary{},
	}
	if initialized := connection.session.InitializeResult(); initialized != nil {
		catalog.ProtocolVersion = initialized.ProtocolVersion
		if initialized.ServerInfo != nil {
			catalog.ServerName = initialized.ServerInfo.Name
			catalog.ServerVersion = initialized.ServerInfo.Version
		}
		if initialized.Capabilities != nil {
			catalog.Extensions = initialized.Capabilities.Extensions
			if initialized.Capabilities.Prompts != nil {
				catalog.Prompts, err = listPromptSummaries(ctx, connection.session)
				if err != nil {
					return ToolCatalog{}, err
				}
			}
			if initialized.Capabilities.Resources != nil {
				catalog.Resources, err = listResourceSummaries(ctx, connection.session)
				if err != nil {
					return ToolCatalog{}, err
				}
				catalog.ResourceTemplates, err = listResourceTemplateSummaries(ctx, connection.session)
				if err != nil {
					return ToolCatalog{}, err
				}
			}
		}
	}
	return catalog, nil
}

func (m *Manager) CallTool(
	ctx context.Context,
	definition ToolDefinition,
	arguments map[string]any,
	providedOptions ...CallOptions,
) (output map[string]any, outputErr error) {
	if definition.session == nil {
		return nil, errors.New("MCP tool is not connected")
	}
	if len(providedOptions) > 1 {
		return nil, errors.New("at most one MCP call options value is supported")
	}
	options := CallOptions{}
	if len(providedOptions) == 1 {
		options = providedOptions[0]
	}
	callContext, callTimeout := newCallInactivityTimeout(ctx, m.toolCallTimeout)
	defer callTimeout.stop()
	call := m.startToolCall(definition, strings.TrimSpace(options.ToolCallID), callTimeout)
	if call != nil {
		callContext = context.WithValue(callContext, toolCallContextKey{}, call)
	}
	if call != nil {
		defer func() {
			m.finishToolCall(call)
			m.emit(Event{
				Type:       EventCallFinished,
				SessionID:  call.sessionID,
				ToolCallID: call.toolCallID,
				ServerID:   call.serverID,
				ServerName: call.serverName,
				ToolName:   call.toolName,
				ToolTitle:  call.toolTitle,
				Output:     terminalToolResult(definition, output, outputErr),
			})
		}()
		m.emit(Event{
			Type:       EventCallStarted,
			SessionID:  call.sessionID,
			ToolCallID: call.toolCallID,
			ServerID:   call.serverID,
			ServerName: call.serverName,
			ToolName:   call.toolName,
			ToolTitle:  call.toolTitle,
			Cancelable: true,
		})
	}
	params := &mcp.CallToolParams{
		Name:      definition.OriginalName,
		Arguments: arguments,
	}
	if call != nil {
		params.SetProgressToken(call.token)
	}
	result, err := definition.session.CallTool(callContext, params)
	if call != nil && m.toolCallWasStopped(call) {
		return cancelledToolResult(definition), nil
	}
	if callTimeout.didTimeOut() && ctx.Err() == nil {
		return timedOutToolResult(definition, m.toolCallTimeout), nil
	}
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q on %q: %w", definition.OriginalName, definition.ServerName, err)
	}
	return mcpToolResult(result, definition)
}

func (m *Manager) CancelToolCall(sessionID, toolCallID string) bool {
	key := toolCallKey(sessionID, toolCallID)
	m.mu.Lock()
	call := m.activeCalls[key]
	if call == nil {
		m.mu.Unlock()
		return false
	}
	call.userStopped = true
	cancel := call.cancel
	m.mu.Unlock()
	cancel()
	return true
}

func (m *Manager) StartOAuth(
	ctx context.Context,
	serverID string,
) (OAuthStart, error) {
	server, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return OAuthStart{}, err
	}
	if server.Transport != store.MCPTransportHTTP || server.AuthType != store.MCPAuthOAuth {
		return OAuthStart{}, fmt.Errorf("%w: MCP server does not use HTTP OAuth", store.ErrInvalidInput)
	}
	if strings.TrimSpace(m.callbackURL) == "" {
		return OAuthStart{}, fmt.Errorf("MCP OAuth callback URL is not configured")
	}

	m.CloseServer(serverID)
	flow, err := m.oauth.begin(serverID)
	if err != nil {
		return OAuthStart{}, err
	}
	go func() {
		background, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
		defer cancel()
		connection, connectErr := m.connection(
			background,
			"definition:"+server.ID,
			"",
			server,
			flow,
		)
		if connectErr == nil {
			_, connectErr = listTools(background, connection.session)
		}
		m.oauth.finish(flow, connectErr)
	}()

	select {
	case <-flow.ready:
		return OAuthStart{
			AuthorizationURL: flow.authorizationURL,
			State:            flow.state,
		}, nil
	case <-flow.done:
		status := m.oauth.status(serverID)
		if status.State == OAuthStateConnected {
			return OAuthStart{Connected: true}, nil
		}
		if status.Error == "" {
			status.Error = "OAuth authorization could not be started"
		}
		return OAuthStart{}, errors.New(status.Error)
	case <-ctx.Done():
		return OAuthStart{}, ctx.Err()
	}
}

func (m *Manager) CompleteOAuth(state, code, oauthError string) error {
	return m.oauth.complete(state, code, oauthError)
}

func (m *Manager) OAuthStatus(ctx context.Context, serverID string) (OAuthStatus, error) {
	server, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return OAuthStatus{}, err
	}
	if server.AuthType != store.MCPAuthOAuth {
		return OAuthStatus{
			State:             OAuthStateNotApplicable,
			CredentialStorage: m.credentials.Backend(),
		}, nil
	}
	return m.oauth.statusWithCredentials(serverID), nil
}

func (m *Manager) DisconnectOAuth(ctx context.Context, serverID string) error {
	if _, err := m.store.GetMCPServer(ctx, serverID); err != nil {
		return err
	}
	m.CloseServer(serverID)
	m.dropOAuthHandlers(serverID)
	if err := m.credentials.Delete(credentialstore.RefreshTokenKey(serverID)); err != nil {
		return err
	}
	if err := m.credentials.Delete(credentialstore.ClientSecretKey(serverID)); err != nil {
		return err
	}
	if err := m.store.DeleteMCPOAuthMetadata(ctx, serverID); err != nil {
		return err
	}
	m.oauth.disconnected(serverID)
	return nil
}

func (m *Manager) CloseSession(sessionID string) {
	m.closeMatching(func(connection *connection) bool {
		return strings.HasPrefix(connection.key, "session:"+sessionID+":")
	})
}

func (m *Manager) CloseServer(serverID string) {
	m.closeMatching(func(connection *connection) bool {
		return connection.serverID == serverID
	})
}

func (m *Manager) ForgetServer(serverID string) error {
	m.CloseServer(serverID)
	m.dropOAuthHandlers(serverID)
	if err := m.credentials.Delete(credentialstore.RefreshTokenKey(serverID)); err != nil {
		return err
	}
	if err := m.credentials.Delete(credentialstore.ClientSecretKey(serverID)); err != nil {
		return err
	}
	m.oauth.disconnected(serverID)
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	m.cancel()
	connections := make([]*connection, 0, len(m.connections))
	for _, current := range m.connections {
		connections = append(connections, current)
	}
	attempts := make([]*connectionAttempt, 0, len(m.connecting))
	for _, attempt := range m.connecting {
		attempt.cancelled = true
		attempt.cancel()
		attempts = append(attempts, attempt)
	}
	m.connections = make(map[string]*connection)
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, current := range connections {
			current.cancel()
			_ = current.session.Close()
		}
		for _, attempt := range attempts {
			<-attempt.done
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) connection(
	ctx context.Context,
	key, workingDirectory string,
	server store.MCPServer,
	flow *oauthFlow,
) (*connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fingerprint, err := serverFingerprint(server, workingDirectory)
	if err != nil {
		return nil, err
	}
	for {
		var stale *connection
		m.mu.Lock()
		if m.stopping {
			m.mu.Unlock()
			return nil, errors.New("MCP manager is shutting down")
		}
		if existing := m.connections[key]; existing != nil {
			if existing.fingerprint == fingerprint {
				m.mu.Unlock()
				return existing, nil
			}
			delete(m.connections, key)
			stale = existing
		}
		if attempt := m.connecting[key]; attempt != nil {
			m.mu.Unlock()
			if stale != nil {
				stale.cancel()
				_ = stale.session.Close()
			}
			select {
			case <-attempt.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		connectionContext, cancelConnection := context.WithCancel(m.ctx)
		stopCallerCancellation := context.AfterFunc(ctx, cancelConnection)
		control := &transportControl{}
		cancelAttempt := func() {
			cancelConnection()
			control.close()
		}
		attempt := &connectionAttempt{
			key:      key,
			serverID: server.ID,
			done:     make(chan struct{}),
			cancel:   cancelAttempt,
		}
		m.connecting[key] = attempt
		m.mu.Unlock()
		if stale != nil {
			stale.cancel()
			_ = stale.session.Close()
		}

		opened, openErr := m.openConnection(
			connectionContext,
			key,
			fingerprint,
			workingDirectory,
			server,
			flow,
			cancelAttempt,
			control,
		)
		stopCallerCancellation()
		m.mu.Lock()
		delete(m.connecting, key)
		cancelled := attempt.cancelled || m.stopping || connectionContext.Err() != nil
		if openErr == nil && !cancelled {
			m.connections[key] = opened
		}
		close(attempt.done)
		m.mu.Unlock()
		if openErr == nil && cancelled {
			opened.cancel()
			_ = opened.session.Close()
			return nil, context.Canceled
		}
		if openErr != nil {
			cancelAttempt()
		}
		return opened, openErr
	}
}

func (m *Manager) openConnection(
	ctx context.Context,
	key, fingerprint, workingDirectory string,
	server store.MCPServer,
	flow *oauthFlow,
	cancel context.CancelFunc,
	control *transportControl,
) (*connection, error) {
	capabilities := &mcp.ClientCapabilities{
		RootsV2: &mcp.RootCapabilities{},
	}
	var root *mcp.Root
	if workingDirectory != "" {
		var err error
		root, err = workspaceRoot(workingDirectory)
		if err != nil {
			return nil, err
		}
	}
	clientOptions := &mcp.ClientOptions{
		Capabilities: capabilities,
		Logger:       slog.Default().With("component", "mcp", "mcp_server_id", server.ID),
		ToolListChangedHandler: func(
			_ context.Context,
			request *mcp.ToolListChangedRequest,
		) {
			go m.refreshToolCatalog(key, server, request.Session)
		},
		LoggingMessageHandler: func(
			_ context.Context,
			request *mcp.LoggingMessageRequest,
		) {
			m.handleLogMessage(key, server, request.Params)
		},
		ProgressNotificationHandler: func(
			_ context.Context,
			request *mcp.ProgressNotificationClientRequest,
		) {
			m.handleProgress(request.Params)
		},
	}
	if m.elicitation != nil {
		capabilities.Elicitation = &mcp.ElicitationCapabilities{
			Form: &mcp.FormElicitationCapabilities{},
			URL:  &mcp.URLElicitationCapabilities{},
		}
		clientOptions.ElicitationHandler = func(
			requestContext context.Context,
			request *mcp.ElicitRequest,
		) (*mcp.ElicitResult, error) {
			return m.handleElicitation(requestContext, key, server, request)
		}
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "MaterialMind", Version: "development"},
		clientOptions,
	)
	if root != nil {
		client.AddRoots(root)
	}
	var transport mcp.Transport
	switch server.Transport {
	case store.MCPTransportStdio:
		resolved, err := exec.LookPath(server.Command)
		if err != nil {
			return nil, fmt.Errorf("MCP command %q is unavailable: %w", server.Command, err)
		}
		command := exec.CommandContext(ctx, resolved, server.Arguments...)
		if workingDirectory != "" {
			command.Dir = workingDirectory
		}
		command.Env, err = resolvedCommandEnvironment(server.Environment)
		if err != nil {
			return nil, err
		}
		command.Stderr = os.Stderr
		transport = &mcp.CommandTransport{Command: command}
	case store.MCPTransportHTTP:
		httpClient, err := configuredHTTPClient(ctx, server, control)
		if err != nil {
			return nil, err
		}
		streamable := &mcp.StreamableClientTransport{
			Endpoint:   server.URL,
			HTTPClient: httpClient,
		}
		if server.AuthType == store.MCPAuthOAuth {
			streamable.OAuthHandler = m.oauthHandler(server, flow)
		}
		transport = streamable
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", server.Transport)
	}
	session, err := client.Connect(
		ctx,
		controlledTransport{base: transport, control: control},
		nil,
	)
	if err != nil {
		return nil, err
	}
	if initialized := session.InitializeResult(); shouldSetLegacyLoggingLevel(initialized) {
		if err := session.SetLoggingLevel(ctx, &mcp.SetLoggingLevelParams{
			Level: mcp.LoggingLevel("info"),
		}); err != nil {
			slog.Warn(
				"set MCP server log level",
				"mcp_server_id",
				server.ID,
				"error",
				err,
			)
		}
	}
	return &connection{
		key:         key,
		serverID:    server.ID,
		fingerprint: fingerprint,
		session:     session,
		cancel:      cancel,
	}, nil
}

func workspaceRoot(workingDirectory string) (*mcp.Root, error) {
	absolute, err := filepath.Abs(workingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP workspace root: %w", err)
	}
	path := filepath.ToSlash(absolute)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	name := filepath.Base(filepath.Clean(absolute))
	if name == "." || name == string(filepath.Separator) {
		name = absolute
	}
	return &mcp.Root{
		URI:  (&url.URL{Scheme: "file", Path: path}).String(),
		Name: name,
	}, nil
}

func shouldSetLegacyLoggingLevel(initialized *mcp.InitializeResult) bool {
	return initialized != nil &&
		initialized.ProtocolVersion < modernProtocolVersion &&
		initialized.Capabilities != nil &&
		initialized.Capabilities.Logging != nil
}

func (t controlledTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if !t.control.set(connection) {
		return nil, context.Canceled
	}
	return connection, nil
}

func (c *transportControl) set(connection mcp.Connection) bool {
	c.mu.Lock()
	if !c.closed {
		c.connection = connection
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	_ = connection.Close()
	return false
}

func (c *transportControl) addCloser(closer func()) {
	c.mu.Lock()
	if !c.closed {
		c.closers = append(c.closers, closer)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	closer()
}

func (c *transportControl) close() {
	c.mu.Lock()
	c.closed = true
	connection := c.connection
	closers := slices.Clone(c.closers)
	c.mu.Unlock()
	for _, closer := range closers {
		closer()
	}
	if connection != nil {
		_ = connection.Close()
	}
}

func (m *Manager) oauthHandler(server store.MCPServer, flow *oauthFlow) *oauthHandler {
	fingerprint, _ := serverFingerprint(server, "")
	key := server.ID + ":" + fingerprint
	m.mu.Lock()
	defer m.mu.Unlock()
	handler := m.oauthHandlers[key]
	if handler == nil {
		handler = newOAuthHandler(
			server,
			m.store,
			m.credentials,
			m.oauth,
			m.callbackURL,
		)
		m.oauthHandlers[key] = handler
	}
	if flow != nil {
		m.oauth.assign(flow)
	}
	return handler
}

func (m *Manager) dropOAuthHandlers(serverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.oauthHandlers {
		if strings.HasPrefix(key, serverID+":") {
			delete(m.oauthHandlers, key)
		}
	}
}

func (m *Manager) closeMatching(matches func(*connection) bool) {
	m.mu.Lock()
	selected := make([]*connection, 0)
	for key, current := range m.connections {
		if matches(current) {
			selected = append(selected, current)
			delete(m.connections, key)
		}
	}
	for _, attempt := range m.connecting {
		if matches(&connection{key: attempt.key, serverID: attempt.serverID}) {
			attempt.cancelled = true
			attempt.cancel()
		}
	}
	m.mu.Unlock()
	for _, current := range selected {
		current.cancel()
		_ = current.session.Close()
	}
}

func (m *Manager) startToolCall(
	definition ToolDefinition,
	toolCallID string,
	timeout *callInactivityTimeout,
) *activeToolCall {
	if definition.sessionID == "" || toolCallID == "" {
		return nil
	}
	token := fmt.Sprintf(
		"materialmind-mcp-%d",
		m.nextCallToken.Add(1),
	)
	call := &activeToolCall{
		key:           toolCallKey(definition.sessionID, toolCallID),
		token:         token,
		sessionID:     definition.sessionID,
		toolCallID:    toolCallID,
		serverID:      definition.ServerID,
		serverName:    definition.ServerName,
		toolName:      definition.OriginalName,
		toolTitle:     definition.Title,
		connectionKey: definition.connectionKey,
		timeout:       timeout,
		cancel:        timeout.cancel,
	}
	m.mu.Lock()
	if m.stopping || m.activeCalls[call.key] != nil {
		m.mu.Unlock()
		return nil
	}
	m.activeCalls[call.key] = call
	m.callsByToken[call.token] = call
	m.mu.Unlock()
	return call
}

func (m *Manager) finishToolCall(call *activeToolCall) {
	m.mu.Lock()
	if m.activeCalls[call.key] == call {
		delete(m.activeCalls, call.key)
	}
	call.cancel = nil
	retainToken := m.callsByToken[call.token] == call
	m.mu.Unlock()
	if retainToken {
		time.AfterFunc(completedCallEventRetention, func() {
			m.mu.Lock()
			if m.callsByToken[call.token] == call {
				delete(m.callsByToken, call.token)
			}
			m.mu.Unlock()
		})
	}
}

func (m *Manager) toolCallWasStopped(call *activeToolCall) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return call.userStopped
}

func (m *Manager) handleProgress(params *mcp.ProgressNotificationParams) {
	if params == nil {
		return
	}
	token, ok := params.ProgressToken.(string)
	if !ok {
		return
	}
	m.mu.Lock()
	call := m.callsByToken[token]
	m.mu.Unlock()
	if call == nil {
		return
	}
	m.emit(Event{
		Type:       EventProgress,
		SessionID:  call.sessionID,
		ToolCallID: call.toolCallID,
		ServerID:   call.serverID,
		ServerName: call.serverName,
		ToolName:   call.toolName,
		ToolTitle:  call.toolTitle,
		Cancelable: true,
		Message:    params.Message,
		Progress:   params.Progress,
		Total:      params.Total,
	})
}

func (m *Manager) handleLogMessage(
	connectionKey string,
	server store.MCPServer,
	params *mcp.LoggingMessageParams,
) {
	if params == nil {
		return
	}
	event := Event{
		Type:       EventLog,
		SessionID:  sessionIDFromConnectionKey(connectionKey),
		ServerID:   server.ID,
		ServerName: server.Name,
		Level:      string(params.Level),
		Logger:     params.Logger,
		Data:       params.Data,
	}
	if token, ok := params.GetProgressToken().(string); ok {
		m.mu.Lock()
		call := m.callsByToken[token]
		m.mu.Unlock()
		if call != nil {
			event.SessionID = call.sessionID
			event.ToolCallID = call.toolCallID
			event.ToolName = call.toolName
			event.ToolTitle = call.toolTitle
		}
	}
	m.emit(event)
}

func (m *Manager) refreshToolCatalog(
	connectionKey string,
	server store.MCPServer,
	session *mcp.ClientSession,
) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	tools, err := listTools(ctx, session)
	event := Event{
		Type:       EventToolsChanged,
		SessionID:  sessionIDFromConnectionKey(connectionKey),
		ServerID:   server.ID,
		ServerName: server.Name,
	}
	if err != nil {
		event.Error = err.Error()
		m.emit(event)
		return
	}

	m.mu.Lock()
	current := m.connections[connectionKey]
	m.mu.Unlock()
	if current == nil || current.session != session {
		return
	}
	previous := current.replaceTools(tools)
	event.Added, event.Removed = changedToolNames(previous, tools)
	event.Count = len(tools)
	m.emit(event)
}

func (m *Manager) emit(event Event) {
	if m.events != nil && event.SessionID != "" {
		m.events(event)
	}
}

func toolCallKey(sessionID, toolCallID string) string {
	return sessionID + "\x00" + toolCallID
}

func sessionIDFromConnectionKey(key string) string {
	const prefix = "session:"
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	remainder := strings.TrimPrefix(key, prefix)
	sessionID, _, ok := strings.Cut(remainder, ":")
	if !ok {
		return ""
	}
	return sessionID
}

func (c *connection) listTools(ctx context.Context, refresh bool) ([]*mcp.Tool, error) {
	if !refresh {
		c.toolsMu.RLock()
		if c.toolsLoaded && c.useConnectionToolCache() {
			tools := slices.Clone(c.tools)
			c.toolsMu.RUnlock()
			return tools, nil
		}
		c.toolsMu.RUnlock()
	}
	tools, err := listTools(ctx, c.session)
	if err != nil {
		return nil, err
	}
	c.replaceTools(tools)
	return slices.Clone(tools), nil
}

func (c *connection) useConnectionToolCache() bool {
	initialized := c.session.InitializeResult()
	return initialized == nil || protocolUsesConnectionToolCache(initialized.ProtocolVersion)
}

func protocolUsesConnectionToolCache(protocolVersion string) bool {
	// The modern SDK owns its per-page TTL cache. A second aggregate cache here
	// would hide page expiry and list-change invalidation from the SDK.
	return protocolVersion < modernProtocolVersion
}

func (c *connection) replaceTools(tools []*mcp.Tool) []*mcp.Tool {
	c.toolsMu.Lock()
	previous := slices.Clone(c.tools)
	c.tools = slices.Clone(tools)
	c.toolsLoaded = true
	c.toolsMu.Unlock()
	return previous
}

func changedToolNames(previous, current []*mcp.Tool) ([]string, []string) {
	previousNames := make(map[string]struct{}, len(previous))
	currentNames := make(map[string]struct{}, len(current))
	for _, tool := range previous {
		previousNames[tool.Name] = struct{}{}
	}
	for _, tool := range current {
		currentNames[tool.Name] = struct{}{}
	}
	added := make([]string, 0)
	removed := make([]string, 0)
	for name := range currentNames {
		if _, ok := previousNames[name]; !ok {
			added = append(added, name)
		}
	}
	for name := range previousNames {
		if _, ok := currentNames[name]; !ok {
			removed = append(removed, name)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}

func listTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var result []*mcp.Tool
	cursor := ""
	for {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		result = append(result, page.Tools...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	slices.SortFunc(result, func(left, right *mcp.Tool) int {
		return strings.Compare(left.Name, right.Name)
	})
	return result, nil
}

func mcpToolResult(
	result *mcp.CallToolResult,
	definition ToolDefinition,
) (map[string]any, error) {
	if result == nil {
		return map[string]any{
			"state": "completed",
			"mcp":   toolResultMetadata(definition, false),
		}, nil
	}
	output := map[string]any{
		"state": "completed",
		"mcp":   toolResultMetadata(definition, result.IsError),
	}
	if result.IsError {
		output["state"] = "error"
	}
	if result.StructuredContent != nil {
		output["structuredContent"] = result.StructuredContent
	}
	if len(result.Content) > 0 {
		content := make([]any, 0, len(result.Content))
		for _, item := range result.Content {
			encoded, err := item.MarshalJSON()
			if err != nil {
				return nil, fmt.Errorf("encode MCP tool content: %w", err)
			}
			var decoded any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				return nil, fmt.Errorf("decode MCP tool content: %w", err)
			}
			content = append(content, decoded)
		}
		output["content"] = content
	}
	return output, nil
}

func cancelledToolResult(definition ToolDefinition) map[string]any {
	return map[string]any{
		"state": "cancelled",
		"mcp":   toolResultMetadata(definition, false),
	}
}

func timedOutToolResult(definition ToolDefinition, timeout time.Duration) map[string]any {
	return map[string]any{
		"state":          "timed_out",
		"timedOut":       true,
		"timeoutSeconds": int64(timeout / time.Second),
		"error":          fmt.Sprintf("MCP tool call timed out after %s", timeout),
		"mcp":            toolResultMetadata(definition, true),
	}
}

func terminalToolResult(
	definition ToolDefinition,
	output map[string]any,
	err error,
) map[string]any {
	if err != nil {
		return map[string]any{
			"state": "error",
			"error": err.Error(),
			"mcp":   toolResultMetadata(definition, true),
		}
	}
	if output != nil {
		return output
	}
	return map[string]any{
		"state": "completed",
		"mcp":   toolResultMetadata(definition, false),
	}
}

func toolResultMetadata(definition ToolDefinition, isError bool) map[string]any {
	metadata := map[string]any{
		"serverId":   definition.ServerID,
		"serverName": definition.ServerName,
		"toolName":   definition.OriginalName,
		"isError":    isError,
	}
	if definition.Title != "" {
		metadata["toolTitle"] = definition.Title
	}
	if definition.UIResourceURI != "" {
		metadata["uiResourceUri"] = definition.UIResourceURI
	}
	return metadata
}

func toolUIResourceURI(remoteTool *mcp.Tool) string {
	if remoteTool == nil {
		return ""
	}
	ui, ok := remoteTool.Meta["ui"].(map[string]any)
	if !ok {
		encoded, err := json.Marshal(remoteTool.Meta["ui"])
		if err != nil || json.Unmarshal(encoded, &ui) != nil {
			return ""
		}
	}
	resourceURI, _ := ui["resourceUri"].(string)
	resourceURI = strings.TrimSpace(resourceURI)
	if !strings.HasPrefix(resourceURI, "ui://") {
		return ""
	}
	return resourceURI
}

func toolTitle(remoteTool *mcp.Tool) string {
	if title := strings.TrimSpace(remoteTool.Title); title != "" {
		return title
	}
	if remoteTool.Annotations != nil {
		if title := strings.TrimSpace(remoteTool.Annotations.Title); title != "" {
			return title
		}
	}
	return remoteTool.Name
}

func resolvedCommandEnvironment(bindings []store.MCPVariableBinding) ([]string, error) {
	result := os.Environ()
	for _, binding := range bindings {
		value, ok := os.LookupEnv(binding.ValueEnvVar)
		if !ok {
			return nil, fmt.Errorf(
				"environment variable %s required for MCP variable %s is not set",
				binding.ValueEnvVar,
				binding.Name,
			)
		}
		result = append(result, binding.Name+"="+value)
	}
	return result, nil
}

func configuredHTTPClient(
	ctx context.Context,
	server store.MCPServer,
	control *transportControl,
) (*http.Client, error) {
	headers := make(http.Header, len(server.Headers)+1)
	for _, binding := range server.Headers {
		value, ok := os.LookupEnv(binding.ValueEnvVar)
		if !ok {
			return nil, fmt.Errorf(
				"environment variable %s required for MCP header %s is not set",
				binding.ValueEnvVar,
				binding.Name,
			)
		}
		headers.Set(binding.Name, value)
	}
	if server.AuthType == store.MCPAuthBearerEnv {
		token := strings.TrimSpace(os.Getenv(server.BearerTokenEnvVar))
		if token == "" {
			return nil, fmt.Errorf(
				"environment variable %s required for MCP bearer authentication is not set",
				server.BearerTokenEnvVar,
			)
		}
		headers.Set("Authorization", "Bearer "+token)
	}
	transport := &headerRoundTripper{
		base:       http.DefaultTransport,
		headers:    headers,
		connection: ctx,
		active:     make(map[*activeHTTPRequest]struct{}),
	}
	control.addCloser(transport.close)
	return &http.Client{Transport: transport}, nil
}

type headerRoundTripper struct {
	base       http.RoundTripper
	headers    http.Header
	connection context.Context
	mu         sync.Mutex
	active     map[*activeHTTPRequest]struct{}
	closed     bool
}

type activeHTTPRequest struct {
	owner  *headerRoundTripper
	cancel context.CancelFunc
	stop   func() bool
	once   sync.Once
}

func (t *headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.connection.Err(); err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithCancel(request.Context())
	active := &activeHTTPRequest{owner: t, cancel: cancel}
	active.stop = context.AfterFunc(t.connection, cancel)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		active.release()
		return nil, context.Canceled
	}
	t.active[active] = struct{}{}
	t.mu.Unlock()

	cloned := request.Clone(requestContext)
	cloned.Header = request.Header.Clone()
	for name, values := range t.headers {
		cloned.Header.Del(name)
		for _, value := range values {
			cloned.Header.Add(name, value)
		}
	}
	response, err := t.base.RoundTrip(cloned)
	if err != nil {
		active.release()
		return nil, err
	}
	response.Body = &cancelOnClose{
		ReadCloser: response.Body,
		cancel:     active.release,
	}
	return response, nil
}

func (t *headerRoundTripper) close() {
	t.mu.Lock()
	t.closed = true
	active := make([]*activeHTTPRequest, 0, len(t.active))
	for request := range t.active {
		active = append(active, request)
	}
	t.mu.Unlock()
	for _, request := range active {
		request.release()
	}
	if transport, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func (r *activeHTTPRequest) release() {
	r.once.Do(func() {
		r.stop()
		r.cancel()
		r.owner.mu.Lock()
		delete(r.owner.active, r)
		r.owner.mu.Unlock()
	})
}

type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func serverFingerprint(server store.MCPServer, workingDirectory string) (string, error) {
	encoded, err := json.Marshal(struct {
		Server           store.MCPServer
		WorkingDirectory string
	}{
		Server:           server,
		WorkingDirectory: workingDirectory,
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint MCP server: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func namespacedToolName(serverID, serverName, toolName string) string {
	serverSlug := toolNameSlug(serverName, 16)
	toolSlug := toolNameSlug(toolName, 24)
	sum := sha256.Sum256([]byte(serverID + "\x00" + toolName))
	return fmt.Sprintf("mcp_%s_%s_%x", serverSlug, toolSlug, sum[:4])
}

func toolNameSlug(value string, limit int) string {
	var result strings.Builder
	lastUnderscore := false
	for _, character := range strings.ToLower(value) {
		valid := character <= unicode.MaxASCII &&
			(character >= 'a' && character <= 'z' ||
				character >= '0' && character <= '9')
		if valid {
			result.WriteRune(character)
			lastUnderscore = false
		} else if !lastUnderscore && result.Len() > 0 {
			result.WriteByte('_')
			lastUnderscore = true
		}
		if result.Len() >= limit {
			break
		}
	}
	return strings.Trim(result.String(), "_")
}
