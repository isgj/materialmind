package engine

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"materialmind/internal/agentskills"
	"materialmind/internal/toolpolicy"
	"materialmind/internal/workspacetools"
)

type approvalResumeModel struct {
	calls int
}

func (m *approvalResumeModel) Name() string {
	return "approval-resume-model"
}

func (m *approvalResumeModel) GenerateContent(
	_ context.Context,
	_ *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{
						ID:   "read-1",
						Name: toolpolicy.ToolReadFile,
						Args: map[string]any{"path": "first.txt"},
					}},
					{FunctionCall: &genai.FunctionCall{
						ID:   "read-2",
						Name: toolpolicy.ToolReadFile,
						Args: map[string]any{"path": "second.txt"},
					}},
				},
			}}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("Both files were read.", genai.RoleModel),
		}, nil)
	}
}

func TestApprovalResumeYieldsBeforeModelWhileSiblingIsPending(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"first.txt":  "first\n",
		"second.txt": "second\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	permissions := toolpolicy.DefaultPermissions()
	for index := range permissions {
		if permissions[index].ToolName == toolpolicy.ToolReadFile {
			permissions[index].ConfirmationMode = toolpolicy.ConfirmationAsk
		}
	}
	tools, err := workspacetools.New(
		root,
		permissions,
		agentskills.Catalog{},
		workspacetools.Options{
			YieldAfterApproval: func(ctx agent.Context) bool {
				return shouldYieldAfterApproval(ctx)
			},
		},
	)
	if err != nil {
		t.Fatalf("workspacetools.New() error = %v", err)
	}
	baseModel := &approvalResumeModel{}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "approval_resume_agent",
		Model: &approvalYieldModel{LLM: baseModel},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(t.Context(), &session.CreateRequest{
		AppName: "approval-resume-test", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: "approval-resume-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	requests := make(map[string]ToolApprovalRequest)
	for event, runErr := range agentRunner.Run(
		t.Context(),
		"user",
		created.Session.ID(),
		genai.NewContentFromText("Read both files", genai.RoleUser),
		agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("first runner.Run() error = %v", runErr)
		}
		eventRequests, requestErr := toolApprovalRequests(event)
		if requestErr != nil {
			t.Fatalf("toolApprovalRequests() error = %v", requestErr)
		}
		for _, request := range eventRequests {
			requests[request.ToolCallID] = request
		}
	}
	if len(requests) != 2 {
		t.Fatalf("approval request count = %d, want 2", len(requests))
	}

	secondEvents := runEngineApprovalTurn(
		t,
		agentRunner,
		created.Session.ID(),
		requests["read-2"],
		true,
	)
	if !hasToolResult(secondEvents, "read-2") {
		t.Fatal("later-approved read-2 did not execute")
	}
	if baseModel.calls != 1 {
		t.Fatalf("model calls after read-2 = %d, want 1", baseModel.calls)
	}

	firstEvents := runEngineApprovalTurn(
		t,
		agentRunner,
		created.Session.ID(),
		requests["read-1"],
		false,
	)
	if !hasToolResult(firstEvents, "read-1") {
		t.Fatal("read-1 did not execute after its approval")
	}
	if baseModel.calls != 2 {
		t.Fatalf("model calls after both reads = %d, want 2", baseModel.calls)
	}
}

func runEngineApprovalTurn(
	t *testing.T,
	agentRunner *runner.Runner,
	sessionID string,
	request ToolApprovalRequest,
	yieldAfterApproval bool,
) []*session.Event {
	t.Helper()
	message := confirmationContent([]ToolApprovalResolution{{
		ID:         request.ID,
		ToolCallID: request.ToolCallID,
		Approved:   true,
		Payload:    request.Payload,
	}})
	var events []*session.Event
	for event, runErr := range agentRunner.Run(
		withApprovalYield(t.Context(), yieldAfterApproval),
		"user",
		sessionID,
		message,
		agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("resume runner.Run() error = %v", runErr)
		}
		events = append(events, event)
	}
	return events
}

func hasToolResult(events []*session.Event, toolCallID string) bool {
	for _, event := range events {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.ID == toolCallID {
				return true
			}
		}
	}
	return false
}
