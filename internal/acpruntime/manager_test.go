package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/store"
)

const acpHelperEnvironment = "MATERIALMIND_ACP_HELPER"

func TestACPPromptContentIncludesEmbeddedAttachment(t *testing.T) {
	content, cleanup, err := acpPromptContent(
		"Review the attachment",
		[]store.RunAttachment{{
			ID:       "attachment-1",
			Name:     "context.go",
			MIMEType: "text/plain",
			Size:     15,
			Content:  []byte("package example"),
		}},
		acp.PromptCapabilities{EmbeddedContext: true},
	)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatalf("acpPromptContent() error = %v", err)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{
		`"type":"resource"`,
		`"text":"package example"`,
		`attachment://attachment-1/context.go`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("prompt JSON does not contain %q: %s", expected, encoded)
		}
	}
}

func TestSupportedAdditionalDirectoriesRequiresAgentCapability(t *testing.T) {
	directories := []string{"/workspace/repository", "/workspace/repository", "relative"}
	if got := supportedAdditionalDirectories(acp.InitializeResponse{}, directories); got != nil {
		t.Fatalf("unsupported additional directories = %#v, want nil", got)
	}
	initialize := acp.InitializeResponse{
		AgentCapabilities: acp.AgentCapabilities{
			SessionCapabilities: acp.SessionCapabilities{
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
			},
		},
	}
	got := supportedAdditionalDirectories(initialize, directories)
	if len(got) != 1 || got[0] != "/workspace/repository" {
		t.Fatalf("supportedAdditionalDirectories() = %#v", got)
	}
}

func TestManagerInspectsAuthenticatesAndListsACPAgentSessions(t *testing.T) {
	t.Setenv(acpHelperEnvironment, "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	manager := New()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	agentRecord := store.ACPAgent{
		ID:        "discovery-agent",
		Name:      "Discovery agent",
		Command:   executable,
		Arguments: []string{"-test.run=^TestACPHelperProcess$"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inspection, err := manager.InspectAgent(ctx, agentRecord)
	if err != nil {
		t.Fatalf("InspectAgent() error = %v", err)
	}
	if inspection.Implementation == nil || inspection.Implementation.Name != "helper-agent" ||
		!inspection.Sessions.List || !inspection.Sessions.Resume ||
		!inspection.Logout || len(inspection.AuthMethods) != 1 || !inspection.AuthMethods[0].Supported {
		t.Fatalf("InspectAgent() = %#v", inspection)
	}
	if _, err := manager.Authenticate(ctx, agentRecord, "helper-auth"); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if _, err := manager.Logout(ctx, agentRecord); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	sessions, err := manager.ListSessions(ctx, agentRecord, "/workspace")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "listed-session" ||
		sessions[0].WorkingDirectory != "/workspace" {
		t.Fatalf("ListSessions() = %#v", sessions)
	}
	state, err := manager.LoadExistingSession(
		ctx,
		agentRecord,
		sessions[0].ID,
		sessions[0].WorkingDirectory,
		nil,
		nil,
		"",
	)
	if err != nil {
		t.Fatalf("LoadExistingSession() error = %v", err)
	}
	if state.ID != "listed-session" || len(state.ConfigOptions) != 1 {
		t.Fatalf("LoadExistingSession() = %#v", state)
	}
}

func TestManagerRunsAndRestoresACPAgentProcess(t *testing.T) {
	t.Setenv(acpHelperEnvironment, "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	manager := New()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	agentRecord := store.ACPAgent{
		ID:        "helper-agent",
		Name:      "Helper agent",
		Command:   executable,
		Arguments: []string{"-test.run=^TestACPHelperProcess$"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	state, err := manager.NewSession(ctx, agentRecord, t.TempDir(), nil, nil, "")
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if state.ID != "helper-session" || len(state.ConfigOptions) != 1 {
		t.Fatalf("NewSession() = %#v", state)
	}

	handler := &recordingACPHandler{}
	response, _, err := manager.Prompt(
		ctx,
		agentRecord,
		state.ID,
		t.TempDir(),
		"Run the helper",
		nil,
		nil,
		nil,
		"",
		state.ConfigOptions,
		handler,
	)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt() stop reason = %q", response.StopReason)
	}
	if updates, permissions := handler.counts(); updates != 4 || permissions != 1 {
		t.Fatalf("handler counts = (%d, %d), want (4, 1)", updates, permissions)
	}
	if handler.elicitationCount() != 1 {
		t.Fatalf("handler elicitation count = %d, want 1", handler.elicitationCount())
	}

	options, err := manager.SetSessionConfigOption(
		ctx,
		agentRecord,
		state.ID,
		t.TempDir(),
		nil,
		nil,
		"",
		state.ConfigOptions,
		"fast",
		false,
	)
	if err != nil {
		t.Fatalf("SetSessionConfigOption() error = %v", err)
	}
	if len(options) != 1 ||
		options[0].Boolean == nil ||
		options[0].Boolean.CurrentValue {
		t.Fatalf("SetSessionConfigOption() = %#v", options)
	}
	_, reappliedOptions, err := manager.Prompt(
		ctx,
		agentRecord,
		state.ID,
		t.TempDir(),
		"Continue with the saved configuration",
		nil,
		nil,
		nil,
		"",
		options,
		&recordingACPHandler{},
	)
	if err != nil {
		t.Fatalf("Prompt() with attached session error = %v", err)
	}
	if len(reappliedOptions) != 1 ||
		reappliedOptions[0].Boolean == nil ||
		reappliedOptions[0].Boolean.CurrentValue {
		t.Fatalf("Prompt() reapplied options = %#v", reappliedOptions)
	}

	manager.StopAgent(agentRecord.ID)
	restoredHandler := &recordingACPHandler{}
	_, restoredOptions, err := manager.Prompt(
		ctx,
		agentRecord,
		state.ID,
		t.TempDir(),
		"Resume the helper",
		nil,
		nil,
		nil,
		"",
		options,
		restoredHandler,
	)
	if err != nil {
		t.Fatalf("Prompt() after restart error = %v", err)
	}
	if len(restoredOptions) != 1 ||
		restoredOptions[0].Boolean == nil ||
		restoredOptions[0].Boolean.CurrentValue {
		t.Fatalf("Prompt() restored options = %#v", restoredOptions)
	}
}

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv(acpHelperEnvironment) != "1" {
		return
	}
	agent := &helperACPAgent{}
	connection := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.connection = connection
	<-connection.Done()
}

type recordingACPHandler struct {
	mu           sync.Mutex
	updates      int
	permissions  int
	elicitations int
	completions  int
}

func (h *recordingACPHandler) RequestElicitation(
	_ context.Context,
	request ElicitationRequest,
) (ElicitationResolution, error) {
	h.mu.Lock()
	h.elicitations++
	h.mu.Unlock()
	return ElicitationResolution{
		ID:      request.ID,
		Action:  ElicitationActionAccept,
		Content: map[string]any{"environment": "test"},
	}, nil
}

func (h *recordingACPHandler) CompleteElicitation(
	context.Context,
	string,
	string,
) error {
	h.mu.Lock()
	h.completions++
	h.mu.Unlock()
	return nil
}

func (h *recordingACPHandler) SessionUpdate(
	context.Context,
	acp.SessionNotification,
) error {
	h.mu.Lock()
	h.updates++
	h.mu.Unlock()
	return nil
}

func (h *recordingACPHandler) RequestPermission(
	_ context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	h.mu.Lock()
	h.permissions++
	h.mu.Unlock()
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeSelected(request.Options[0].OptionId),
	}, nil
}

func (h *recordingACPHandler) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updates, h.permissions
}

func (h *recordingACPHandler) elicitationCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.elicitations
}

func (h *recordingACPHandler) completionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.completions
}

type helperACPAgent struct {
	connection *acp.AgentSideConnection
}

func (*helperACPAgent) Authenticate(
	context.Context,
	acp.AuthenticateRequest,
) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*helperACPAgent) Initialize(
	_ context.Context,
	request acp.InitializeRequest,
) (acp.InitializeResponse, error) {
	if !request.ClientCapabilities.Terminal {
		return acp.InitializeResponse{}, errors.New("client did not advertise terminal support")
	}
	if request.ClientCapabilities.Elicitation == nil ||
		request.ClientCapabilities.Elicitation.Form == nil ||
		request.ClientCapabilities.Elicitation.Url == nil {
		return acp.InitializeResponse{}, errors.New("client did not advertise elicitation support")
	}
	if !request.ClientCapabilities.Fs.ReadTextFile || !request.ClientCapabilities.Fs.WriteTextFile {
		return acp.InitializeResponse{}, errors.New("client did not advertise filesystem support")
	}
	if request.ClientCapabilities.PlanCapabilities == nil {
		return acp.InitializeResponse{}, errors.New("client did not advertise plan support")
	}
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    "helper-agent",
			Version: "1.0.0",
		},
		AuthMethods: []acp.AuthMethod{{
			Agent: &acp.AuthMethodAgent{Id: "helper-auth", Name: "Helper authentication"},
		}},
		AgentCapabilities: acp.AgentCapabilities{
			Auth: acp.AgentAuthCapabilities{Logout: &acp.LogoutCapabilities{}},
			SessionCapabilities: acp.SessionCapabilities{
				Close:  &acp.SessionCloseCapabilities{},
				List:   &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
			},
		},
	}, nil
}

func (*helperACPAgent) Logout(
	context.Context,
	acp.LogoutRequest,
) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*helperACPAgent) Cancel(context.Context, acp.CancelNotification) error {
	return nil
}

func (*helperACPAgent) CloseSession(
	context.Context,
	acp.CloseSessionRequest,
) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (*helperACPAgent) ListSessions(
	_ context.Context,
	request acp.ListSessionsRequest,
) (acp.ListSessionsResponse, error) {
	cwd := "/workspace"
	if request.Cwd != nil {
		cwd = *request.Cwd
	}
	title := "Listed session"
	return acp.ListSessionsResponse{Sessions: []acp.SessionInfo{{
		SessionId: "listed-session",
		Cwd:       cwd,
		Title:     &title,
	}}}, nil
}

func (*helperACPAgent) NewSession(
	context.Context,
	acp.NewSessionRequest,
) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{
		SessionId:     "helper-session",
		ConfigOptions: helperACPConfigOptions(true),
	}, nil
}

func (a *helperACPAgent) Prompt(
	ctx context.Context,
	request acp.PromptRequest,
) (acp.PromptResponse, error) {
	elicitation := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{
		Type: acp.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{
			"environment": map[string]any{
				"type": "string",
				"enum": []string{"test", "production"},
			},
		},
		Required: []string{"environment"},
	})
	elicitation.Form.Message = "Choose the environment"
	elicitationResponse, err := a.connection.UnstableCreateElicitation(ctx, elicitation)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if elicitationResponse.Accept == nil ||
		elicitationResponse.Accept.Content["environment"] != "test" {
		return acp.PromptResponse{}, fmt.Errorf(
			"unexpected elicitation response %#v",
			elicitationResponse,
		)
	}
	if err := a.connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: request.SessionId,
		Update:    acp.UpdateAgentThoughtText("Checking permission."),
	}); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: request.SessionId,
		Update: acp.StartToolCall(
			"helper-tool",
			"Run helper command",
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartRawInput(map[string]any{"command": "true"}),
		),
	}); err != nil {
		return acp.PromptResponse{}, err
	}
	permission, err := a.connection.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: request.SessionId,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: "helper-tool",
			Kind:       acp.Ptr(acp.ToolKindExecute),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
		},
		Options: []acp.PermissionOption{
			{
				OptionId: "allow",
				Name:     "Allow",
				Kind:     acp.PermissionOptionKindAllowOnce,
			},
			{
				OptionId: "reject",
				Name:     "Reject",
				Kind:     acp.PermissionOptionKindRejectOnce,
			},
		},
	})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	status := acp.ToolCallStatusFailed
	rawOutput := map[string]any{"exit_code": 1}
	if permission.Outcome.Selected != nil &&
		permission.Outcome.Selected.OptionId == "allow" {
		output, terminalErr := a.runTerminal(ctx, request.SessionId)
		if terminalErr != nil {
			return acp.PromptResponse{}, terminalErr
		}
		status = acp.ToolCallStatusCompleted
		rawOutput = map[string]any{
			"exit_code":        0,
			"formatted_output": output,
		}
	}
	if err := a.connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: request.SessionId,
		Update: acp.UpdateToolCall(
			"helper-tool",
			acp.WithUpdateStatus(status),
			acp.WithUpdateRawOutput(rawOutput),
		),
	}); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: request.SessionId,
		Update:    acp.UpdateAgentMessageText("Done."),
	}); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *helperACPAgent) runTerminal(
	ctx context.Context,
	sessionID acp.SessionId,
) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	terminal, err := a.connection.CreateTerminal(ctx, acp.CreateTerminalRequest{
		SessionId: sessionID,
		Command:   executable,
		Args:      []string{"-test.run=^TestACPTerminalHelperProcess$"},
		Env: []acp.EnvVariable{
			{Name: acpTerminalHelperEnvironment, Value: "truncate"},
		},
	})
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = a.connection.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  sessionID,
			TerminalId: terminal.TerminalId,
		})
	}()
	exit, err := a.connection.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{
		SessionId:  sessionID,
		TerminalId: terminal.TerminalId,
	})
	if err != nil {
		return "", err
	}
	if exit.ExitCode == nil || *exit.ExitCode != 0 {
		return "", fmt.Errorf("terminal exited with status %#v", exit)
	}
	output, err := a.connection.TerminalOutput(ctx, acp.TerminalOutputRequest{
		SessionId:  sessionID,
		TerminalId: terminal.TerminalId,
	})
	if err != nil {
		return "", err
	}
	return output.Output, nil
}

func (*helperACPAgent) ResumeSession(
	context.Context,
	acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{ConfigOptions: helperACPConfigOptions(true)}, nil
}

func (*helperACPAgent) SetSessionConfigOption(
	_ context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	value := true
	if request.Boolean != nil {
		value = request.Boolean.Value
	}
	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: helperACPConfigOptions(value),
	}, nil
}

func (*helperACPAgent) SetSessionMode(
	context.Context,
	acp.SetSessionModeRequest,
) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func helperACPConfigOptions(value bool) []acp.SessionConfigOption {
	option := acp.NewSessionConfigOptionBoolean(value)
	option.Boolean.Id = "fast"
	option.Boolean.Name = "Fast mode"
	return []acp.SessionConfigOption{option}
}
