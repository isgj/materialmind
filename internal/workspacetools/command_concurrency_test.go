package workspacetools

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"materialmind/internal/toolpolicy"
)

type concurrentCommandApprovalModel struct {
	mu          sync.Mutex
	calls       int
	executable  string
	releaseFile string
}

func (m *concurrentCommandApprovalModel) Name() string {
	return "concurrent-command-approval-model"
}

func (m *concurrentCommandApprovalModel) GenerateContent(
	_ context.Context,
	_ *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		m.calls++
		call := m.calls
		m.mu.Unlock()
		if call == 1 {
			parts := make([]*genai.Part, 0, 3)
			for _, name := range []string{"first", "second", "third"} {
				parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
					ID:   "command-" + name,
					Name: toolpolicy.ToolRunCommand,
					Args: map[string]any{
						"command": m.executable,
						"args": []string{
							"-test.run=^TestRunCommandHelper$",
							"--",
							"wait-file",
							name,
							m.releaseFile,
						},
					},
				}})
			}
			yield(&model.LLMResponse{Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: parts,
			}}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("All commands completed.", genai.RoleModel),
		}, nil)
	}
}

func (m *concurrentCommandApprovalModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestApprovedCommandsStartWithoutWaitingForRunningSiblings(t *testing.T) {
	workspace := t.TempDir()
	releaseFile := filepath.Join(workspace, "release")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = os.WriteFile(releaseFile, []byte("release"), 0o600)
	})

	approvalRequests := make(chan ToolApprovalRequest, 3)
	decisions := map[string]chan ToolApprovalDecision{
		"command-first":  make(chan ToolApprovalDecision, 1),
		"command-second": make(chan ToolApprovalDecision, 1),
		"command-third":  make(chan ToolApprovalDecision, 1),
	}
	var approvedCalls sync.Map
	requestApproval := func(
		approvalContext context.Context,
		request ToolApprovalRequest,
	) (ToolApprovalDecision, error) {
		select {
		case approvalRequests <- request:
		case <-approvalContext.Done():
			return ToolApprovalDecision{}, approvalContext.Err()
		}
		decisionChannel, ok := decisions[request.ToolCallID]
		if !ok {
			return ToolApprovalDecision{}, context.Canceled
		}
		select {
		case decision := <-decisionChannel:
			if decision.Approved {
				approvedCalls.Store(request.ToolCallID, true)
			}
			return decision, nil
		case <-approvalContext.Done():
			return ToolApprovalDecision{}, approvalContext.Err()
		}
	}
	outputEvents := make(chan CommandOutputEvent, 32)
	preapprovalOutput := make(chan string, 1)
	commandTool, err := newRunCommandTool(
		workspace,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		func(event CommandOutputEvent) {
			if _, approved := approvedCalls.Load(event.ToolCallID); !approved {
				select {
				case preapprovalOutput <- event.ToolCallID:
				default:
				}
			}
			outputEvents <- event
		},
		nil,
		requestApproval,
	)
	if err != nil {
		t.Fatalf("newRunCommandTool() error = %v", err)
	}

	fakeModel := &concurrentCommandApprovalModel{
		executable:  os.Args[0],
		releaseFile: releaseFile,
	}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "concurrent_command_agent",
		Model: fakeModel,
		Tools: []tool.Tool{commandTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "concurrent-command-test",
		UserID:  "user",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName:        "concurrent-command-test",
		Agent:          agentInstance,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		for _, runErr := range agentRunner.Run(
			ctx,
			"user",
			created.Session.ID(),
			genai.NewContentFromText("Run all three commands", genai.RoleUser),
			agent.RunConfig{},
		) {
			if runErr != nil {
				runDone <- runErr
				return
			}
		}
		runDone <- nil
	}()

	requests := make(map[string]ToolApprovalRequest, 3)
	for len(requests) < 3 {
		select {
		case request := <-approvalRequests:
			requests[request.ToolCallID] = request
		case runErr := <-runDone:
			t.Fatalf("runner completed before all approvals were requested: %v", runErr)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for approval requests: %v", ctx.Err())
		}
	}
	for callID, request := range requests {
		if request.ToolName != toolpolicy.ToolRunCommand {
			t.Fatalf("approval %s tool = %q", callID, request.ToolName)
		}
		if request.Payload["kind"] != toolpolicy.ToolRunCommand {
			t.Fatalf("approval %s payload = %#v", callID, request.Payload)
		}
	}
	select {
	case runErr := <-runDone:
		t.Fatalf("runner completed while approvals were pending: %v", runErr)
	default:
	}

	for _, name := range []string{"first", "second", "third"} {
		callID := "command-" + name
		decisions[callID] <- ToolApprovalDecision{Approved: true}
		waitForCommandStart(t, ctx, outputEvents, callID, name)
		select {
		case runErr := <-runDone:
			t.Fatalf("runner completed before commands were released: %v", runErr)
		default:
		}
	}
	if err := os.WriteFile(releaseFile, []byte("release"), 0o600); err != nil {
		t.Fatalf("release commands: %v", err)
	}
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("runner error = %v", runErr)
		}
	case <-ctx.Done():
		t.Fatalf("runner did not complete after commands were released: %v", ctx.Err())
	}
	if calls := fakeModel.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
	select {
	case callID := <-preapprovalOutput:
		t.Fatalf("command %s produced output before its approval handler returned", callID)
	default:
	}
}

func waitForCommandStart(
	t *testing.T,
	ctx context.Context,
	events <-chan CommandOutputEvent,
	toolCallID string,
	name string,
) {
	t.Helper()
	var output strings.Builder
	for {
		select {
		case event := <-events:
			if event.ToolCallID != toolCallID || event.Stream != "stdout" {
				continue
			}
			output.WriteString(event.Text)
			if strings.Contains(output.String(), "started:"+name) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("command %s did not start while its siblings were running: %v", name, ctx.Err())
		}
	}
}
