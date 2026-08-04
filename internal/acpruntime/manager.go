package acpruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/store"
)

var ErrClientCapabilityUnsupported = errors.New("ACP client capability is not enabled")

type Handler interface {
	SessionUpdate(context.Context, acp.SessionNotification) error
	RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

type FileSystemHandler interface {
	ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error)
	WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error)
}

type ElicitationHandler interface {
	RequestElicitation(context.Context, ElicitationRequest) (ElicitationResolution, error)
}

type ElicitationCompletionHandler interface {
	CompleteElicitation(context.Context, string, string) error
}

type TerminalOutputEvent struct {
	TerminalID string
	Stream     string
	Text       string
}

type TerminalOutputHandler interface {
	TerminalOutput(TerminalOutputEvent)
}

type SessionState struct {
	ID            string
	ConfigOptions []acp.SessionConfigOption
}

type MCPBridgeOptions struct {
	Command                string
	DatabasePath           string
	CredentialStoreMode    string
	CredentialStoreBackend func() string
}

type InternalMCPOptions struct {
	Command  string
	Endpoint string
}

type Options struct {
	MCPBridge   MCPBridgeOptions
	InternalMCP InternalMCPOptions
}

type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	processes   map[string]*agentProcess
	mcpBridge   MCPBridgeOptions
	internalMCP InternalMCPOptions
}

type agentProcess struct {
	agent       store.ACPAgent
	fingerprint string
	command     *exec.Cmd
	connection  *acp.ClientSideConnection
	client      *client
	initialize  acp.InitializeResponse
	done        chan struct{}

	mu       sync.Mutex
	sessions map[acp.SessionId]struct{}
}

func New(provided ...Options) *Manager {
	options := Options{}
	if len(provided) > 0 {
		options = provided[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		ctx:         ctx,
		cancel:      cancel,
		processes:   make(map[string]*agentProcess),
		mcpBridge:   options.MCPBridge,
		internalMCP: options.InternalMCP,
	}
}

func (m *Manager) NewSession(
	ctx context.Context,
	agent store.ACPAgent,
	workingDirectory string,
	additionalDirectories []string,
	mcpServers []store.SessionMCPServer,
	internalMCPToken string,
) (SessionState, error) {
	process, err := m.process(ctx, agent)
	if err != nil {
		return SessionState{}, err
	}
	acpMCPServers, err := configuredMCPServers(
		process.initialize,
		mcpServers,
		m.mcpBridge,
		m.internalMCP,
		internalMCPToken,
	)
	if err != nil {
		return SessionState{}, err
	}
	response, err := process.connection.NewSession(ctx, acp.NewSessionRequest{
		Cwd:                   workingDirectory,
		AdditionalDirectories: supportedAdditionalDirectories(process.initialize, additionalDirectories),
		McpServers:            acpMCPServers,
	})
	if err != nil {
		return SessionState{}, fmt.Errorf("create ACP session with %q: %w", agent.Name, err)
	}
	process.mu.Lock()
	process.sessions[response.SessionId] = struct{}{}
	process.mu.Unlock()
	return SessionState{
		ID:            string(response.SessionId),
		ConfigOptions: response.ConfigOptions,
	}, nil
}

func (m *Manager) Prompt(
	ctx context.Context,
	agent store.ACPAgent,
	sessionID, workingDirectory, message string,
	attachments []store.RunAttachment,
	additionalDirectories []string,
	mcpServers []store.SessionMCPServer,
	internalMCPToken string,
	preferredConfigOptions []acp.SessionConfigOption,
	handler Handler,
) (acp.PromptResponse, []acp.SessionConfigOption, error) {
	process, options, restored, err := m.attach(
		ctx,
		agent,
		sessionID,
		workingDirectory,
		additionalDirectories,
		mcpServers,
		internalMCPToken,
		handler,
	)
	if err != nil {
		return acp.PromptResponse{}, nil, err
	}
	defer process.client.unregister(acp.SessionId(sessionID), handler)
	options, err = restoreSessionConfigOptions(
		ctx,
		process.connection,
		agent.Name,
		acp.SessionId(sessionID),
		options,
		preferredConfigOptions,
		!restored,
	)
	if err != nil {
		return acp.PromptResponse{}, options, err
	}

	prompt, cleanup, err := acpPromptContent(
		message,
		attachments,
		process.initialize.AgentCapabilities.PromptCapabilities,
	)
	if err != nil {
		return acp.PromptResponse{}, options, err
	}
	defer cleanup()
	response, err := process.connection.Prompt(ctx, acp.PromptRequest{
		SessionId: acp.SessionId(sessionID),
		Prompt:    prompt,
	})
	if err != nil {
		return acp.PromptResponse{}, options, fmt.Errorf("run ACP prompt with %q: %w", agent.Name, err)
	}
	return response, options, nil
}

func acpPromptContent(
	message string,
	attachments []store.RunAttachment,
	capabilities acp.PromptCapabilities,
) ([]acp.ContentBlock, func(), error) {
	content := make([]acp.ContentBlock, 0, len(attachments)+1)
	if message != "" {
		content = append(content, acp.TextBlock(message))
	}
	var temporaryDirectory string
	cleanup := func() {
		if temporaryDirectory != "" {
			_ = os.RemoveAll(temporaryDirectory)
		}
	}
	for index, attachment := range attachments {
		encoded := base64.StdEncoding.EncodeToString(attachment.Content)
		if strings.HasPrefix(attachment.MIMEType, "image/") && capabilities.Image {
			content = append(content, acp.ImageBlock(encoded, attachment.MIMEType))
			continue
		}
		if capabilities.EmbeddedContext {
			uri := (&url.URL{
				Scheme: "attachment",
				Host:   attachment.ID,
				Path:   "/" + attachment.Name,
			}).String()
			mimeType := attachment.MIMEType
			if strings.HasPrefix(attachment.MIMEType, "text/") ||
				strings.HasPrefix(attachment.MIMEType, "application/") &&
					attachment.MIMEType != "application/pdf" {
				content = append(content, acp.ResourceBlock(acp.EmbeddedResourceResource{
					TextResourceContents: &acp.TextResourceContents{
						MimeType: &mimeType,
						Text:     string(attachment.Content),
						Uri:      uri,
					},
				}))
			} else {
				content = append(content, acp.ResourceBlock(acp.EmbeddedResourceResource{
					BlobResourceContents: &acp.BlobResourceContents{
						Blob:     encoded,
						MimeType: &mimeType,
						Uri:      uri,
					},
				}))
			}
			continue
		}
		if temporaryDirectory == "" {
			var err error
			temporaryDirectory, err = os.MkdirTemp("", "materialmind-acp-attachments-*")
			if err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("create ACP attachment directory: %w", err)
			}
		}
		filename := fmt.Sprintf("%02d-%s", index+1, attachment.Name)
		filePath := filepath.Join(temporaryDirectory, filename)
		if err := os.WriteFile(filePath, attachment.Content, 0o600); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write ACP attachment %q: %w", attachment.Name, err)
		}
		block := acp.ResourceLinkBlock(
			attachment.Name,
			(&url.URL{Scheme: "file", Path: filePath}).String(),
		)
		mimeType := attachment.MIMEType
		size := int(attachment.Size)
		block.ResourceLink.MimeType = &mimeType
		block.ResourceLink.Size = &size
		content = append(content, block)
	}
	return content, cleanup, nil
}

func (m *Manager) SetSessionConfigOption(
	ctx context.Context,
	agent store.ACPAgent,
	sessionID, workingDirectory string,
	additionalDirectories []string,
	mcpServers []store.SessionMCPServer,
	internalMCPToken string,
	preferredConfigOptions []acp.SessionConfigOption,
	configID string,
	value any,
) ([]acp.SessionConfigOption, error) {
	process, options, restored, err := m.attach(
		ctx,
		agent,
		sessionID,
		workingDirectory,
		additionalDirectories,
		mcpServers,
		internalMCPToken,
		nil,
	)
	if err != nil {
		return nil, err
	}
	options, err = restoreSessionConfigOptions(
		ctx,
		process.connection,
		agent.Name,
		acp.SessionId(sessionID),
		options,
		preferredConfigOptions,
		!restored,
	)
	if err != nil {
		return nil, err
	}
	request, err := newSetSessionConfigOptionRequest(acp.SessionId(sessionID), configID, value)
	if err != nil {
		return nil, err
	}
	response, err := process.connection.SetSessionConfigOption(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("set ACP session configuration with %q: %w", agent.Name, err)
	}
	return response.ConfigOptions, nil
}

func (m *Manager) CloseSession(ctx context.Context, agentID, sessionID string) error {
	m.mu.Lock()
	process := m.processes[agentID]
	m.mu.Unlock()
	if process == nil || process.initialize.AgentCapabilities.SessionCapabilities.Close == nil {
		return nil
	}
	if _, err := process.connection.CloseSession(ctx, acp.CloseSessionRequest{
		SessionId: acp.SessionId(sessionID),
	}); err != nil {
		return fmt.Errorf("close ACP session: %w", err)
	}
	process.client.closeSessionTerminals(acp.SessionId(sessionID))
	process.mu.Lock()
	delete(process.sessions, acp.SessionId(sessionID))
	process.mu.Unlock()
	return nil
}

func (m *Manager) StopAgent(agentID string) {
	m.mu.Lock()
	process := m.processes[agentID]
	delete(m.processes, agentID)
	m.mu.Unlock()
	if process != nil && process.command.Process != nil {
		stopProcess(process.command)
	}
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.cancel()
	m.mu.Lock()
	processes := make([]*agentProcess, 0, len(m.processes))
	for _, process := range m.processes {
		processes = append(processes, process)
		if process.command.Process != nil {
			stopProcess(process.command)
		}
	}
	m.mu.Unlock()

	for _, process := range processes {
		select {
		case <-process.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *Manager) attach(
	ctx context.Context,
	agent store.ACPAgent,
	sessionID, workingDirectory string,
	additionalDirectories []string,
	mcpServers []store.SessionMCPServer,
	internalMCPToken string,
	handler Handler,
) (*agentProcess, []acp.SessionConfigOption, bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil, false, fmt.Errorf("%w: ACP session ID is missing", store.ErrInvalidInput)
	}
	process, err := m.process(ctx, agent)
	if err != nil {
		return nil, nil, false, err
	}
	acpSessionID := acp.SessionId(sessionID)
	process.client.register(ctx, acpSessionID, workingDirectory, handler)

	process.mu.Lock()
	_, attached := process.sessions[acpSessionID]
	process.mu.Unlock()
	if attached {
		return process, nil, false, nil
	}
	acpMCPServers, err := configuredMCPServers(
		process.initialize,
		mcpServers,
		m.mcpBridge,
		m.internalMCP,
		internalMCPToken,
	)
	if err != nil {
		process.client.unregister(acpSessionID, handler)
		return nil, nil, false, err
	}

	var options []acp.SessionConfigOption
	capabilities := process.initialize.AgentCapabilities
	switch {
	case capabilities.SessionCapabilities.Resume != nil:
		response, resumeErr := process.connection.ResumeSession(ctx, acp.ResumeSessionRequest{
			SessionId:             acpSessionID,
			Cwd:                   workingDirectory,
			AdditionalDirectories: supportedAdditionalDirectories(process.initialize, additionalDirectories),
			McpServers:            acpMCPServers,
		})
		if resumeErr != nil {
			process.client.unregister(acpSessionID, handler)
			return nil, nil, false, fmt.Errorf("resume ACP session with %q: %w", agent.Name, resumeErr)
		}
		options = response.ConfigOptions
	case capabilities.LoadSession:
		response, loadErr := process.connection.LoadSession(ctx, acp.LoadSessionRequest{
			SessionId:             acpSessionID,
			Cwd:                   workingDirectory,
			AdditionalDirectories: supportedAdditionalDirectories(process.initialize, additionalDirectories),
			McpServers:            acpMCPServers,
		})
		if loadErr != nil {
			process.client.unregister(acpSessionID, handler)
			return nil, nil, false, fmt.Errorf("load ACP session with %q: %w", agent.Name, loadErr)
		}
		options = response.ConfigOptions
	default:
		process.client.unregister(acpSessionID, handler)
		return nil, nil, false, fmt.Errorf(
			"%w: ACP agent %q cannot restore a session after its process restarts",
			store.ErrInvalidInput,
			agent.Name,
		)
	}

	process.mu.Lock()
	process.sessions[acpSessionID] = struct{}{}
	process.mu.Unlock()
	return process, options, true, nil
}

func supportedAdditionalDirectories(
	initialize acp.InitializeResponse,
	directories []string,
) []string {
	if initialize.AgentCapabilities.SessionCapabilities.AdditionalDirectories == nil {
		return nil
	}
	result := make([]string, 0, len(directories))
	for _, directory := range directories {
		directory = filepath.Clean(strings.TrimSpace(directory))
		if directory == "." || !filepath.IsAbs(directory) || slices.Contains(result, directory) {
			continue
		}
		result = append(result, directory)
	}
	return result
}

func restoreSessionConfigOptions(
	ctx context.Context,
	connection *acp.ClientSideConnection,
	agentName string,
	sessionID acp.SessionId,
	current, preferred []acp.SessionConfigOption,
	force bool,
) ([]acp.SessionConfigOption, error) {
	if len(preferred) == 0 {
		return current, nil
	}
	if len(current) == 0 {
		current = slices.Clone(preferred)
	}
	ordered := slices.Clone(preferred)
	slices.SortStableFunc(ordered, func(left, right acp.SessionConfigOption) int {
		return sessionConfigOptionPriority(left) - sessionConfigOptionPriority(right)
	})
	for _, desired := range ordered {
		available := findSessionConfigOption(current, sessionConfigOptionID(desired))
		request, shouldSet := sessionConfigRestoreRequest(sessionID, available, desired, force)
		if !shouldSet {
			continue
		}
		response, err := connection.SetSessionConfigOption(ctx, request)
		if err != nil {
			return current, fmt.Errorf(
				"restore ACP session configuration %q with %q: %w",
				sessionConfigOptionID(desired),
				agentName,
				err,
			)
		}
		if len(response.ConfigOptions) > 0 {
			current = response.ConfigOptions
		} else {
			current = replaceSessionConfigOption(current, desired)
		}
	}
	return current, nil
}

func sessionConfigRestoreRequest(
	sessionID acp.SessionId,
	available, desired acp.SessionConfigOption,
	force bool,
) (acp.SetSessionConfigOptionRequest, bool) {
	switch {
	case available.Select != nil && desired.Select != nil:
		value := desired.Select.CurrentValue
		if !sessionConfigSelectHasValue(available.Select.Options, value) ||
			!force && available.Select.CurrentValue == value {
			return acp.SetSessionConfigOptionRequest{}, false
		}
		request, err := newSetSessionConfigOptionRequest(
			sessionID,
			string(desired.Select.Id),
			string(value),
		)
		return request, err == nil
	case available.Boolean != nil && desired.Boolean != nil:
		value := desired.Boolean.CurrentValue
		if !force && available.Boolean.CurrentValue == value {
			return acp.SetSessionConfigOptionRequest{}, false
		}
		request, err := newSetSessionConfigOptionRequest(
			sessionID,
			string(desired.Boolean.Id),
			value,
		)
		return request, err == nil
	default:
		return acp.SetSessionConfigOptionRequest{}, false
	}
}

func newSetSessionConfigOptionRequest(
	sessionID acp.SessionId,
	configID string,
	value any,
) (acp.SetSessionConfigOptionRequest, error) {
	switch typed := value.(type) {
	case bool:
		return acp.SetSessionConfigOptionRequest{
			Boolean: &acp.SetSessionConfigOptionBoolean{
				ConfigId:  acp.SessionConfigId(configID),
				SessionId: sessionID,
				Type:      "boolean",
				Value:     typed,
			},
		}, nil
	case string:
		return acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				ConfigId:  acp.SessionConfigId(configID),
				SessionId: sessionID,
				Value:     acp.SessionConfigValueId(typed),
			},
		}, nil
	default:
		return acp.SetSessionConfigOptionRequest{}, fmt.Errorf(
			"%w: ACP configuration values must be strings or booleans",
			store.ErrInvalidInput,
		)
	}
}

func sessionConfigOptionPriority(option acp.SessionConfigOption) int {
	if option.Select == nil || option.Select.Category == nil {
		return 2
	}
	switch *option.Select.Category {
	case acp.SessionConfigOptionCategoryModel:
		return 0
	case acp.SessionConfigOptionCategoryThoughtLevel:
		return 1
	default:
		return 2
	}
}

func sessionConfigOptionID(option acp.SessionConfigOption) string {
	if option.Select != nil {
		return string(option.Select.Id)
	}
	if option.Boolean != nil {
		return string(option.Boolean.Id)
	}
	return ""
}

func findSessionConfigOption(
	options []acp.SessionConfigOption,
	id string,
) acp.SessionConfigOption {
	for _, option := range options {
		if sessionConfigOptionID(option) == id {
			return option
		}
	}
	return acp.SessionConfigOption{}
}

func replaceSessionConfigOption(
	options []acp.SessionConfigOption,
	replacement acp.SessionConfigOption,
) []acp.SessionConfigOption {
	result := slices.Clone(options)
	id := sessionConfigOptionID(replacement)
	for index, option := range result {
		if sessionConfigOptionID(option) == id {
			result[index] = replacement
			return result
		}
	}
	return append(result, replacement)
}

func sessionConfigSelectHasValue(
	options acp.SessionConfigSelectOptions,
	value acp.SessionConfigValueId,
) bool {
	if options.Ungrouped != nil &&
		slices.ContainsFunc(*options.Ungrouped, func(option acp.SessionConfigSelectOption) bool {
			return option.Value == value
		}) {
		return true
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			if slices.ContainsFunc(group.Options, func(option acp.SessionConfigSelectOption) bool {
				return option.Value == value
			}) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) process(ctx context.Context, agent store.ACPAgent) (*agentProcess, error) {
	fingerprint := agent.Command + "\x00" + strings.Join(agent.Arguments, "\x00")

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.processes[agent.ID]; existing != nil {
		select {
		case <-existing.done:
			delete(m.processes, agent.ID)
		default:
			if existing.fingerprint == fingerprint {
				return existing, nil
			}
			if existing.command.Process != nil {
				stopProcess(existing.command)
			}
			delete(m.processes, agent.ID)
		}
	}

	resolvedCommand, err := exec.LookPath(agent.Command)
	if err != nil {
		return nil, fmt.Errorf("%w: ACP command %q is not available", store.ErrInvalidInput, agent.Command)
	}
	command := exec.CommandContext(m.ctx, resolvedCommand, agent.Arguments...)
	configureProcess(command)
	command.Env = os.Environ()
	command.Stderr = os.Stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open ACP agent stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open ACP agent stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start ACP agent %q: %w", agent.Name, err)
	}

	client := newClient()
	connection := acp.NewClientSideConnection(client, stdin, stdout)
	connection.SetLogger(slog.Default().With("component", "acp", "acp_agent_id", agent.ID))
	process := &agentProcess{
		agent:       agent,
		fingerprint: fingerprint,
		command:     command,
		connection:  connection,
		client:      client,
		done:        make(chan struct{}),
		sessions:    make(map[acp.SessionId]struct{}),
	}
	go func() {
		err := command.Wait()
		process.client.closeTerminals()
		if err != nil && m.ctx.Err() == nil {
			slog.Warn("ACP agent process stopped", "agent_id", agent.ID, "error", err)
		}
		close(process.done)
		m.mu.Lock()
		if m.processes[agent.ID] == process {
			delete(m.processes, agent.ID)
		}
		m.mu.Unlock()
	}()

	initialize, err := connection.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{
				Form: &acp.ElicitationFormCapabilities{},
				Url:  &acp.ElicitationUrlCapabilities{},
			},
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			PlanCapabilities: &acp.PlanCapabilities{},
			Terminal:         true,
		},
		ClientInfo: &acp.Implementation{
			Name:    "MaterialMind",
			Title:   acp.Ptr("MaterialMind"),
			Version: "development",
		},
	})
	if err != nil {
		stopProcess(command)
		return nil, fmt.Errorf("initialize ACP agent %q: %w", agent.Name, err)
	}
	if initialize.ProtocolVersion != acp.ProtocolVersionNumber {
		stopProcess(command)
		return nil, fmt.Errorf(
			"%w: ACP agent %q negotiated unsupported protocol version %d",
			store.ErrInvalidInput,
			agent.Name,
			initialize.ProtocolVersion,
		)
	}
	process.initialize = initialize
	m.processes[agent.ID] = process
	return process, nil
}
