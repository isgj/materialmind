package engine

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

func TestMixedToolBatchModelDefersDelegationsTogether(t *testing.T) {
	base := &mixedToolBatchScriptedModel{}
	scheduled := &mixedToolBatchModel{LLM: base}

	first := collectModelResponses(t, scheduled.GenerateContent(
		t.Context(),
		&model.LLMRequest{},
		true,
	))
	if got := functionCallNames(first[0]); !equalStrings(
		got,
		[]string{"ordinary_tool", "grep"},
	) {
		t.Fatalf("first call names = %#v", got)
	}
	if base.callCount() != 1 {
		t.Fatalf("base calls after ordinary batch = %d, want 1", base.callCount())
	}

	second := collectModelResponses(t, scheduled.GenerateContent(
		t.Context(),
		&model.LLMRequest{},
		true,
	))
	if got := functionCallNames(second[0]); !equalStrings(
		got,
		[]string{"workspace_explorer", "code_reviewer"},
	) {
		t.Fatalf("deferred call names = %#v", got)
	}
	if second[0].UsageMetadata != nil {
		t.Fatal("deferred synthetic response repeats usage metadata")
	}
	if base.callCount() != 1 {
		t.Fatalf("base calls after deferred batch = %d, want 1", base.callCount())
	}

	third := collectModelResponses(t, scheduled.GenerateContent(
		t.Context(),
		&model.LLMRequest{},
		true,
	))
	if third[0].Content.Parts[0].Text != "done" {
		t.Fatalf("third response = %#v", third[0].Content)
	}
	if base.callCount() != 2 {
		t.Fatalf("base calls after final response = %d, want 2", base.callCount())
	}
}

func TestMixedToolBatchKeepsDelegationsPendingAcrossApprovalYield(t *testing.T) {
	base := &mixedToolBatchScriptedModel{}
	scheduled := &mixedToolBatchModel{LLM: base}
	coordinated := &approvalYieldModel{LLM: scheduled}

	collectModelResponses(t, coordinated.GenerateContent(
		t.Context(),
		&model.LLMRequest{},
		true,
	))

	yielded := 0
	for response, err := range coordinated.GenerateContent(
		withApprovalYield(t.Context(), true),
		&model.LLMRequest{},
		true,
	) {
		if err != nil {
			t.Fatalf("approval-yield GenerateContent() error = %v", err)
		}
		if response != nil {
			yielded++
		}
	}
	if yielded != 0 {
		t.Fatalf("responses during approval yield = %d, want 0", yielded)
	}

	deferred := collectModelResponses(t, coordinated.GenerateContent(
		t.Context(),
		&model.LLMRequest{},
		true,
	))
	if got := functionCallNames(deferred[0]); !equalStrings(
		got,
		[]string{"workspace_explorer", "code_reviewer"},
	) {
		t.Fatalf("deferred call names after approval yield = %#v", got)
	}
	if base.callCount() != 1 {
		t.Fatalf("base calls after approval yield = %d, want 1", base.callCount())
	}
}

func TestMixedToolBatchPublishesOrdinaryResultBeforeDelegationCompletes(t *testing.T) {
	fastTool, err := functiontool.New(
		functiontool.Config{Name: "ordinary_tool", Description: "returns immediately"},
		func(agent.Context, struct{}) (map[string]any, error) {
			return map[string]any{"state": "complete"}, nil
		},
	)
	if err != nil {
		t.Fatalf("create ordinary tool: %v", err)
	}
	delegationStarted := make(chan struct{})
	releaseDelegation := make(chan struct{})
	delegationTool, err := functiontool.New(
		functiontool.Config{
			Name:        "workspace_explorer",
			Description: "blocks until the test releases it",
		},
		func(ctx agent.Context, _ struct{}) (map[string]any, error) {
			close(delegationStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-releaseDelegation:
				return map[string]any{"result": "complete"}, nil
			}
		},
	)
	if err != nil {
		t.Fatalf("create delegation tool: %v", err)
	}

	scheduled := &mixedToolBatchModel{LLM: &mixedToolBatchScriptedModel{}}
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:  "workspace_agent",
		Model: scheduled,
		Tools: []tool.Tool{fastTool, delegationTool},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(t.Context(), &session.CreateRequest{
		AppName: "test",
		UserID:  "user",
	})
	if err != nil {
		t.Fatalf("create runner session: %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName:        "test",
		Agent:          rootAgent,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	events := make(chan *session.Event, 16)
	runErrors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event, runErr := range agentRunner.Run(
			t.Context(),
			"user",
			created.Session.ID(),
			genai.NewContentFromText("run", genai.RoleUser),
			agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
		) {
			if runErr != nil {
				runErrors <- runErr
				return
			}
			events <- event
		}
	}()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	ordinaryResultSeen := false
	for !ordinaryResultSeen {
		select {
		case event := <-events:
			ordinaryResultSeen = hasFunctionResponse(event, "ordinary-call")
		case runErr := <-runErrors:
			t.Fatalf("runner error before ordinary result: %v", runErr)
		case <-timeout.C:
			t.Fatal("ordinary result was not published while delegation was pending")
		}
	}
	select {
	case <-delegationStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("delegation did not start")
	}
	select {
	case <-done:
		t.Fatal("runner completed before the delegation was released")
	default:
	}

	close(releaseDelegation)
	select {
	case runErr := <-runErrors:
		t.Fatalf("runner error after delegation release: %v", runErr)
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not complete after delegation release")
	}
}

type mixedToolBatchScriptedModel struct {
	mu    sync.Mutex
	calls int
}

func (m *mixedToolBatchScriptedModel) Name() string {
	return "mixed-tool-batch-test"
}

func (m *mixedToolBatchScriptedModel) GenerateContent(
	_ context.Context,
	_ *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		m.calls++
		call := m.calls
		m.mu.Unlock()
		if call > 1 {
			yield(&model.LLMResponse{
				Content: genai.NewContentFromText("done", genai.RoleModel),
			}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					genai.NewPartFromText("working"),
					{FunctionCall: &genai.FunctionCall{
						ID: "ordinary-call", Name: "ordinary_tool",
					}},
					{FunctionCall: &genai.FunctionCall{
						ID: "delegation-1", Name: "workspace_explorer",
					}},
					{FunctionCall: &genai.FunctionCall{
						ID: "grep-call", Name: "grep",
					}},
					{FunctionCall: &genai.FunctionCall{
						ID: "delegation-2", Name: "code_reviewer",
					}},
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				TotalTokenCount: 10,
			},
		}, nil)
	}
}

func (m *mixedToolBatchScriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func collectModelResponses(
	t *testing.T,
	sequence iter.Seq2[*model.LLMResponse, error],
) []*model.LLMResponse {
	t.Helper()
	var responses []*model.LLMResponse
	for response, err := range sequence {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 1 {
		t.Fatalf("response count = %d, want 1", len(responses))
	}
	return responses
}

func functionCallNames(response *model.LLMResponse) []string {
	var names []string
	if response == nil || response.Content == nil {
		return names
	}
	for _, part := range response.Content.Parts {
		if part != nil && part.FunctionCall != nil {
			names = append(names, part.FunctionCall.Name)
		}
	}
	return names
}

func hasFunctionResponse(event *session.Event, id string) bool {
	if event == nil || event.Content == nil {
		return false
	}
	for _, part := range event.Content.Parts {
		if part != nil && part.FunctionResponse != nil &&
			part.FunctionResponse.ID == id {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
