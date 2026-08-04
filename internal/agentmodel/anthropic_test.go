package agentmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestAnthropicUsesPerConfigurationEndpointAndBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		envName  string
		token    string
		wantAuth string
	}{
		{name: "first configuration", envName: "MATERIALMIND_FIRST_TOKEN", token: "first-token", wantAuth: "Bearer first-token"},
		{name: "second configuration", envName: "MATERIALMIND_SECOND_TOKEN", token: "second-token", wantAuth: "Bearer second-token"},
		{name: "authentication omitted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.envName != "" {
				t.Setenv(test.envName, test.token)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != test.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, test.wantAuth)
				}
				if got := r.Header.Get("X-Api-Key"); got != "" {
					t.Errorf("X-Api-Key = %q, want empty", got)
				}
				var requestBody struct {
					Model     string `json:"model"`
					MaxTokens int    `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request body: %v", err)
				} else if requestBody.Model != "claude-test" {
					t.Errorf("model = %q, want claude-test", requestBody.Model)
				} else if requestBody.MaxTokens != 8192 {
					t.Errorf("max_tokens = %d, want 8192", requestBody.MaxTokens)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`)
			}))
			defer server.Close()

			adapter, err := NewAnthropic("claude-test", server.URL, test.envName, GenerationSettings{
				MaxOutputTokens: 8192,
			})
			if err != nil {
				t.Fatalf("NewAnthropic() error = %v", err)
			}
			if got := adapter.Name(); got != "claude-test" {
				t.Fatalf("Name() = %q, want claude-test", got)
			}
			var response *model.LLMResponse
			for item, generateErr := range adapter.GenerateContent(context.Background(), &model.LLMRequest{
				Model:    adapter.Name(),
				Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
			}, false) {
				if generateErr != nil {
					t.Fatalf("GenerateContent() error = %v", generateErr)
				}
				response = item
			}
			if response == nil || response.Content.Parts[0].Text != "Hello" {
				t.Fatalf("GenerateContent() response = %#v", response)
			}
		})
	}
}

func TestAnthropicReportsConfiguredEnvironmentVariableWhenMissing(t *testing.T) {
	const environmentName = "MATERIALMIND_MISSING_TOKEN"
	t.Setenv(environmentName, "")
	_, err := NewAnthropic("claude-test", "", environmentName, GenerationSettings{MaxOutputTokens: 4096})
	if err == nil || !strings.Contains(err.Error(), environmentName) {
		t.Fatalf("NewAnthropic() error = %v", err)
	}
}

func TestAnthropicRejectsUnsupportedReasoningEffort(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			_, err := NewAnthropic(
				"claude-test",
				"",
				"",
				GenerationSettings{MaxOutputTokens: 4096, ReasoningEffort: &effort},
			)
			if err == nil || !strings.Contains(err.Error(), effort) {
				t.Fatalf("NewAnthropic() error = %v, want unsupported effort %q", err, effort)
			}
		})
	}
}

func TestAnthropicListsModels(t *testing.T) {
	const environmentName = "MATERIALMIND_ANTHROPIC_MODEL_LIST_TOKEN"
	t.Setenv(environmentName, "catalog-token")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-used")
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer catalog-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("X-Api-Key = %q, want empty", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Errorf("Anthropic-Version = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch afterID := r.URL.Query().Get("after_id"); afterID {
		case "":
			fmt.Fprint(w, `{"data":[{"id":"claude-zeta","display_name":"Claude Zeta","created_at":"2026-01-02T00:00:00Z","max_input_tokens":200000,"max_tokens":64000,"type":"model","capabilities":{}}],"first_id":"claude-zeta","has_more":true,"last_id":"claude-zeta"}`)
		case "claude-zeta":
			fmt.Fprint(w, `{"data":[{"id":"claude-alpha","display_name":"Claude Alpha","created_at":"2026-01-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000,"type":"model","capabilities":{}}],"first_id":"claude-alpha","has_more":false,"last_id":"claude-alpha"}`)
		default:
			t.Errorf("after_id = %q", afterID)
		}
	}))
	defer server.Close()

	provider, err := NewProvider(ProviderConfig{
		Compatibility:     CompatibilityAnthropic,
		BaseURL:           server.URL,
		BearerTokenEnvVar: environmentName,
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	models, err := provider.(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if len(models) != 2 || models[0].ID != "claude-alpha" || models[0].DisplayName != "Claude Alpha" ||
		models[0].ContextWindowTokens != 200_000 || models[0].MaxOutputTokens != 64_000 ||
		models[1].ID != "claude-zeta" {
		t.Fatalf("ListModels() = %#v", models)
	}
}

func TestToAnthropicRequest(t *testing.T) {
	temperature := float32(0.2)
	reasoningEffort := "medium"
	request, err := toAnthropicRequest(&model.LLMRequest{
		Model: "claude-test",
		Contents: []*genai.Content{
			genai.NewContentFromText("Inspect the project", genai.RoleUser),
			genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID: "tool-1", Name: "read_file", Args: map[string]any{"path": "go.mod"},
			}}}, genai.RoleModel),
			genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID: "tool-1", Name: "read_file", Response: map[string]any{"content": "module test"},
			}}}, genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("Be precise", genai.RoleUser),
			Temperature:       &temperature,
			MaxOutputTokens:   1234,
			StopSequences:     []string{"IGNORED"},
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
				taskAgentToolDeclaration("read_file"),
			}}},
		},
	}, "fallback", GenerationSettings{
		MaxOutputTokens: 4096,
		ReasoningEffort: &reasoningEffort,
	})
	if err != nil {
		t.Fatalf("toAnthropicRequest() error = %v", err)
	}
	if request.Model != "claude-test" || request.MaxTokens != 1234 {
		t.Fatalf("request model/max tokens = %q/%d", request.Model, request.MaxTokens)
	}
	if len(request.Messages) != 3 || len(request.Tools) != 1 || len(request.System) != 1 {
		t.Fatalf("request shape = messages:%d tools:%d system:%d", len(request.Messages), len(request.Tools), len(request.System))
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{"read_file", "tool-1", "Inspect the project", "input_schema"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"output_config":{"effort":"medium"}`) {
		t.Fatalf("request JSON does not contain Anthropic effort: %s", encoded)
	}
	for _, removed := range []string{`"temperature"`, `"top_p"`, `"top_k"`, `"stop_sequences"`} {
		if strings.Contains(string(encoded), removed) {
			t.Fatalf("request JSON contains removed setting %q: %s", removed, encoded)
		}
	}
	for _, invalid := range []string{`"type":"OBJECT"`, `"type":"STRING"`} {
		if strings.Contains(string(encoded), invalid) {
			t.Fatalf("request JSON contains invalid schema type %q: %s", invalid, encoded)
		}
	}
}

func TestToAnthropicRequestIncludesAttachment(t *testing.T) {
	attachment := genai.NewPartFromBytes([]byte("package example"), "text/plain")
	attachment.InlineData.DisplayName = "context.go"
	request, err := toAnthropicRequest(&model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				{Text: "Review the attachment"},
				attachment,
			}, genai.RoleUser),
		},
	}, "claude-test", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("toAnthropicRequest() error = %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{
		`"type":"document"`,
		`"title":"context.go"`,
		`"data":"package example"`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
}

func TestFromAnthropicMessage(t *testing.T) {
	message := &anthropic.Message{
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []anthropic.ContentBlockUnion{
			{Type: "text", Text: "Checking."},
			{Type: "tool_use", ID: "tool-1", Name: "list_directory", Input: json.RawMessage(`{"path":"."}`)},
		},
		Usage: anthropic.Usage{InputTokens: 10, OutputTokens: 4},
	}
	response := fromAnthropicMessage(message, false, true)
	if len(response.Content.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(response.Content.Parts))
	}
	if response.Content.Parts[1].FunctionCall.Name != "list_directory" {
		t.Fatalf("function call = %#v", response.Content.Parts[1].FunctionCall)
	}
	if response.UsageMetadata.TotalTokenCount != 14 || !response.TurnComplete {
		t.Fatalf("response metadata = %#v", response)
	}
}

func TestFromAnthropicMessagePreservesMalformedFunctionArguments(t *testing.T) {
	message := &anthropic.Message{
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []anthropic.ContentBlockUnion{
			{Type: "tool_use", ID: "tool-1", Name: "edit_file", Input: json.RawMessage(`{"patch":`)},
		},
	}
	response := fromAnthropicMessage(message, false, true)
	call := response.Content.Parts[0].FunctionCall
	decodeError, malformed := FunctionArgumentsDecodeError(call.Args)
	if !malformed || !strings.Contains(decodeError, "unexpected end of JSON input") {
		t.Fatalf("decode error = %q, malformed = %t", decodeError, malformed)
	}
}
