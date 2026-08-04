package workspacetools

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

type approvalOrderArgs struct {
	Name string `json:"name"`
}

type approvalOrderResult struct {
	Name string `json:"name"`
}

type approvalOrderModel struct {
	calls       int
	shouldYield func() bool
}

func (m *approvalOrderModel) Name() string {
	return "approval-order-model"
}

func (m *approvalOrderModel) GenerateContent(
	_ context.Context,
	_ *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.shouldYield != nil && m.shouldYield() {
			return
		}
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{
						ID:   "call-1",
						Name: "secure_action",
						Args: map[string]any{"name": "first"},
					}},
					{FunctionCall: &genai.FunctionCall{
						ID:   "call-2",
						Name: "secure_action",
						Args: map[string]any{"name": "second"},
					}},
				},
			}}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("All approved actions completed.", genai.RoleModel),
		}, nil)
	}
}

func TestApprovalYieldRunsLaterApprovedCallBeforePendingSibling(t *testing.T) {
	var executed []string
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name:                "secure_action",
			Description:         "Test action requiring confirmation.",
			RequireConfirmation: true,
		},
		func(_ agent.Context, args approvalOrderArgs) (approvalOrderResult, error) {
			executed = append(executed, args.Name)
			return approvalOrderResult(args), nil
		},
	)
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	yieldAfterApproval := false
	wrappedTool, err := newApprovalYieldTool(baseTool, func(agent.Context) bool {
		return yieldAfterApproval
	})
	if err != nil {
		t.Fatalf("newApprovalYieldTool() error = %v", err)
	}
	fakeModel := &approvalOrderModel{shouldYield: func() bool {
		return yieldAfterApproval
	}}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "approval_order_agent",
		Model: fakeModel,
		Tools: []tool.Tool{wrappedTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(t.Context(), &session.CreateRequest{
		AppName: "approval-order-test", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: "approval-order-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	confirmations := make(map[string]*genai.FunctionCall)
	for event, runErr := range agentRunner.Run(
		t.Context(),
		"user",
		created.Session.ID(),
		genai.NewContentFromText("Run both actions", genai.RoleUser),
		agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("first runner.Run() error = %v", runErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionCall == nil || part.FunctionCall.Name != toolconfirmation.FunctionCallName {
				continue
			}
			original, originalErr := toolconfirmation.OriginalCallFrom(part.FunctionCall)
			if originalErr != nil {
				t.Fatalf("toolconfirmation.OriginalCallFrom() error = %v", originalErr)
			}
			confirmations[original.ID] = part.FunctionCall
		}
	}
	if len(confirmations) != 2 {
		t.Fatalf("confirmation count = %d, want 2", len(confirmations))
	}

	yieldAfterApproval = true
	secondEvents := runApprovalTurn(
		t,
		agentRunner,
		created.Session.ID(),
		confirmations["call-2"].ID,
	)
	if len(executed) != 1 || executed[0] != "second" {
		t.Fatalf("executed after second approval = %#v, want [second]", executed)
	}
	if fakeModel.calls != 1 {
		t.Fatalf("model calls after second approval = %d, want 1", fakeModel.calls)
	}
	if !hasSkipSummarizationToolResult(secondEvents, "call-2") {
		t.Fatal("second approval did not yield after its tool result")
	}

	yieldAfterApproval = false
	runApprovalTurn(
		t,
		agentRunner,
		created.Session.ID(),
		confirmations["call-1"].ID,
	)
	if len(executed) != 2 || executed[0] != "second" || executed[1] != "first" {
		t.Fatalf("final execution order = %#v, want [second first]", executed)
	}
	if fakeModel.calls != 2 {
		t.Fatalf("model calls after all approvals = %d, want 2", fakeModel.calls)
	}
}

func runApprovalTurn(
	t *testing.T,
	agentRunner *runner.Runner,
	sessionID, approvalID string,
) []*session.Event {
	t.Helper()
	response := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:   approvalID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": true,
			},
		},
	}}}
	var events []*session.Event
	for event, runErr := range agentRunner.Run(
		t.Context(),
		"user",
		sessionID,
		response,
		agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("resume runner.Run() error = %v", runErr)
		}
		events = append(events, event)
	}
	return events
}

func hasSkipSummarizationToolResult(events []*session.Event, toolCallID string) bool {
	for _, event := range events {
		if event == nil || event.Content == nil || !event.Actions.SkipSummarization {
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
