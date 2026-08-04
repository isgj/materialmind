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
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

type fetchHITLModel struct {
	calls        int
	toolResponse map[string]any
}

func (m *fetchHITLModel) Name() string {
	return "fetch-hitl-model"
}

func (m *fetchHITLModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   "fetch-call-1",
					Name: "fetch_url",
					Args: map[string]any{"url": "https://example.com/docs"},
				}}},
			}}, nil)
			return
		}
		for _, content := range request.Contents {
			if content == nil {
				continue
			}
			for _, part := range content.Parts {
				if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Name == "fetch_url" {
					m.toolResponse = part.FunctionResponse.Response
				}
			}
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("The fetch was refused.", genai.RoleModel)}, nil)
	}
}

func TestFetchToolADKConfirmationRoundTrip(t *testing.T) {
	ctx := t.Context()
	fetchTool, err := newFetchTool()
	if err != nil {
		t.Fatalf("newFetchTool() error = %v", err)
	}
	fakeModel := &fetchHITLModel{}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "fetch_agent",
		Model: fakeModel,
		Tools: []tool.Tool{fetchTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "fetch-test", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: "fetch-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	var confirmationCall *genai.FunctionCall
	for event, runErr := range agentRunner.Run(
		ctx, "user", created.Session.ID(), genai.NewContentFromText("Fetch the docs", genai.RoleUser), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("first runner.Run() error = %v", runErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.Name == toolconfirmation.FunctionCallName {
				confirmationCall = part.FunctionCall
			}
		}
	}
	if confirmationCall == nil {
		t.Fatal("first runner.Run() emitted no confirmation call")
	}
	if fakeModel.calls != 1 {
		t.Fatalf("model calls after approval request = %d, want 1", fakeModel.calls)
	}

	response := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:   confirmationCall.ID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": false,
				"payload":   map[string]any{"reason": "Use the checked-in documentation."},
			},
		},
	}}}
	var denial map[string]any
	for event, runErr := range agentRunner.Run(ctx, "user", created.Session.ID(), response, agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("resume runner.Run() error = %v", runErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.Name == "fetch_url" {
				denial = part.FunctionResponse.Response
			}
		}
	}
	if denial["state"] != "denied" || denial["reason"] != "Use the checked-in documentation." {
		t.Fatalf("fetch denial response = %#v", denial)
	}
	if fakeModel.calls != 2 {
		t.Fatalf("model calls after resume = %d, want 2", fakeModel.calls)
	}
	if fakeModel.toolResponse["reason"] != "Use the checked-in documentation." {
		t.Fatalf("tool response passed to model = %#v", fakeModel.toolResponse)
	}
}
