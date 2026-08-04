package workspacetools

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
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

type editHITLModel struct {
	calls        int
	toolResponse map[string]any
}

func (m *editHITLModel) Name() string {
	return "edit-hitl-model"
}

func (m *editHITLModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   "edit-call-1",
					Name: "edit_file",
					Args: map[string]any{
						"changes": []any{
							map[string]any{
								"operation": "update",
								"path":      "main.go",
								"edits": []any{map[string]any{
									"oldText": "hello",
									"newText": "hello, world",
								}},
							},
							map[string]any{
								"operation": "create",
								"path":      "created.txt",
								"content":   "created by the agent\n",
							},
							map[string]any{
								"operation": "delete",
								"path":      "obsolete.txt",
							},
						},
					},
				}}},
			}}, nil)
			return
		}
		for _, content := range request.Contents {
			if content == nil {
				continue
			}
			for _, part := range content.Parts {
				if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Name == "edit_file" {
					m.toolResponse = part.FunctionResponse.Response
				}
			}
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("The edit was applied.", genai.RoleModel)}, nil)
	}
}

func TestEditFileToolADKConfirmationRoundTrip(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	createdPath := filepath.Join(root, "created.txt")
	obsoletePath := filepath.Join(root, "obsolete.txt")
	before := "package main\n\nconst greeting = \"hello\"\n"
	if err := os.WriteFile(filePath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obsoletePath, []byte("obsolete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	editTool, err := newEditFileTool(root)
	if err != nil {
		t.Fatalf("newEditFileTool() error = %v", err)
	}
	fakeModel := &editHITLModel{}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "edit_agent",
		Model: fakeModel,
		Tools: []tool.Tool{editTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "edit-test", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: "edit-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	var confirmationCall *genai.FunctionCall
	for event, runErr := range agentRunner.Run(
		ctx, "user", created.Session.ID(), genai.NewContentFromText("Update the greeting", genai.RoleUser), agent.RunConfig{},
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
	assertFileContent(t, filePath, before)
	assertFileMissing(t, createdPath)
	assertFileContent(t, obsoletePath, "obsolete\n")
	payload := confirmationPayloadFromCall(t, confirmationCall)
	payload["reason"] = ""
	response := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:   confirmationCall.ID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": true,
				"payload":   payload,
			},
		},
	}}}
	for _, runErr := range agentRunner.Run(ctx, "user", created.Session.ID(), response, agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("resume runner.Run() error = %v", runErr)
		}
	}
	assertFileContent(t, filePath, "package main\n\nconst greeting = \"hello, world\"\n")
	assertFileContent(t, createdPath, "created by the agent\n")
	assertFileMissing(t, obsoletePath)
	if fakeModel.calls != 2 || fakeModel.toolResponse["state"] != "applied" {
		t.Fatalf("model calls = %d, tool response = %#v", fakeModel.calls, fakeModel.toolResponse)
	}
}

func confirmationPayloadFromCall(t *testing.T, call *genai.FunctionCall) map[string]any {
	t.Helper()
	value, ok := call.Args["toolConfirmation"]
	if !ok {
		t.Fatal("tool confirmation payload is missing")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var confirmation toolconfirmation.ToolConfirmation
	if err := json.Unmarshal(encoded, &confirmation); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(confirmation.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
