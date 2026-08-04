package engine

import (
	"context"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

type malformedFunctionArgumentsModel struct {
	calls             int
	sawRecoveryResult bool
}

func (*malformedFunctionArgumentsModel) Name() string {
	return "malformed-function-arguments-model"
}

func (m *malformedFunctionArgumentsModel) GenerateContent(
	_ context.Context,
	request *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   "tool-1",
					Name: "edit_file",
					Args: map[string]any{
						"__materialmind_function_arguments_decode_error": "unexpected end of JSON input",
					},
				}}},
			}}, nil)
			return
		}
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse == nil {
					continue
				}
				message, _ := part.FunctionResponse.Response["error"].(string)
				m.sawRecoveryResult = strings.Contains(message, "was not run") &&
					strings.Contains(message, "Retry the tool call")
			}
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("Recovered.", genai.RoleModel),
		}, nil)
	}
}

func TestMalformedFunctionArgumentsReturnToolResultWithoutRunningTool(t *testing.T) {
	type editArgs struct {
		Patch string `json:"patch"`
	}
	toolCalls := 0
	editTool, err := functiontool.New(
		functiontool.Config{Name: "edit_file", Description: "edits a file"},
		func(agent.Context, editArgs) (map[string]any, error) {
			toolCalls++
			return map[string]any{"edited": true}, nil
		},
	)
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	scriptedModel := &malformedFunctionArgumentsModel{}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:                "workspace_agent",
		Model:               scriptedModel,
		Tools:               []tool.Tool{editTool},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{rejectMalformedFunctionArguments},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(t.Context(), &session.CreateRequest{
		AppName: "tool-recovery-test", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: "tool-recovery-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	for _, runErr := range agentRunner.Run(
		t.Context(),
		"user",
		created.Session.ID(),
		genai.NewContentFromText("Edit the file", genai.RoleUser),
		agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("runner.Run() error = %v", runErr)
		}
	}
	if toolCalls != 0 {
		t.Fatalf("tool calls = %d, want 0", toolCalls)
	}
	if scriptedModel.calls != 2 || !scriptedModel.sawRecoveryResult {
		t.Fatalf("model calls = %d, saw recovery result = %t", scriptedModel.calls, scriptedModel.sawRecoveryResult)
	}
}
