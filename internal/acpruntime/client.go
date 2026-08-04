package acpruntime

import (
	"context"
	"fmt"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

type client struct {
	mu           sync.RWMutex
	sessions     map[acp.SessionId]clientSession
	elicitations map[acp.UnstableElicitationId]pendingElicitation

	terminalMu sync.RWMutex
	terminals  map[string]*clientTerminal
}

type clientSession struct {
	ctx              context.Context
	workingDirectory string
	handler          Handler
}

type pendingElicitation struct {
	requestID string
	handler   ElicitationHandler
}

func newClient() *client {
	return &client{
		sessions:     make(map[acp.SessionId]clientSession),
		elicitations: make(map[acp.UnstableElicitationId]pendingElicitation),
		terminals:    make(map[string]*clientTerminal),
	}
}

func (c *client) register(
	ctx context.Context,
	sessionID acp.SessionId,
	workingDirectory string,
	handler Handler,
) {
	if handler == nil {
		return
	}
	c.mu.Lock()
	c.sessions[sessionID] = clientSession{
		ctx:              ctx,
		workingDirectory: workingDirectory,
		handler:          handler,
	}
	c.mu.Unlock()
}

func (c *client) unregister(sessionID acp.SessionId, handler Handler) {
	if handler == nil {
		return
	}
	c.mu.Lock()
	removed := false
	if c.sessions[sessionID].handler == handler {
		delete(c.sessions, sessionID)
		if elicitationHandler, ok := handler.(ElicitationHandler); ok {
			for elicitationID, pending := range c.elicitations {
				if pending.handler == elicitationHandler {
					delete(c.elicitations, elicitationID)
				}
			}
		}
		removed = true
	}
	c.mu.Unlock()
	if removed {
		c.closeSessionTerminals(sessionID)
	}
}

func (c *client) handler(sessionID acp.SessionId) Handler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[sessionID].handler
}

func (c *client) session(sessionID acp.SessionId) (clientSession, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

func (c *client) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	handler := c.handler(params.SessionId)
	if handler == nil {
		return nil
	}
	return handler.SessionUpdate(ctx, params)
}

func (c *client) RequestPermission(
	ctx context.Context,
	params acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	handler := c.handler(params.SessionId)
	if handler == nil {
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeCancelled(),
		}, nil
	}
	return handler.RequestPermission(ctx, params)
}

func (c *client) ReadTextFile(
	ctx context.Context,
	request acp.ReadTextFileRequest,
) (acp.ReadTextFileResponse, error) {
	handler, ok := c.handler(request.SessionId).(FileSystemHandler)
	if !ok {
		return acp.ReadTextFileResponse{}, unsupported("fs/read_text_file")
	}
	return handler.ReadTextFile(ctx, request)
}

func (c *client) WriteTextFile(
	ctx context.Context,
	request acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	handler, ok := c.handler(request.SessionId).(FileSystemHandler)
	if !ok {
		return acp.WriteTextFileResponse{}, unsupported("fs/write_text_file")
	}
	return handler.WriteTextFile(ctx, request)
}

func (c *client) CreateTerminal(
	ctx context.Context,
	request acp.CreateTerminalRequest,
) (acp.CreateTerminalResponse, error) {
	return c.createTerminal(ctx, request)
}

func (c *client) KillTerminal(
	ctx context.Context,
	request acp.KillTerminalRequest,
) (acp.KillTerminalResponse, error) {
	return c.killTerminal(ctx, request)
}

func (c *client) TerminalOutput(
	ctx context.Context,
	request acp.TerminalOutputRequest,
) (acp.TerminalOutputResponse, error) {
	return c.terminalOutput(ctx, request)
}

func (c *client) ReleaseTerminal(
	ctx context.Context,
	request acp.ReleaseTerminalRequest,
) (acp.ReleaseTerminalResponse, error) {
	return c.releaseTerminal(ctx, request)
}

func (c *client) WaitForTerminalExit(
	ctx context.Context,
	request acp.WaitForTerminalExitRequest,
) (acp.WaitForTerminalExitResponse, error) {
	return c.waitForTerminalExit(ctx, request)
}

func unsupported(method string) error {
	return fmt.Errorf("%w: %s", ErrClientCapabilityUnsupported, method)
}
