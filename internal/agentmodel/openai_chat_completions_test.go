package agentmodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestOpenAIChatCompletionsUsesConfiguredEndpointAndBearerToken(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	tests := []struct {
		name     string
		envName  string
		token    string
		wantAuth string
	}{
		{name: "configured authentication", envName: "MATERIALMIND_OPENAI_TOKEN", token: "configured-token", wantAuth: "Bearer configured-token"},
		{name: "authentication omitted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.envName != "" {
				t.Setenv(test.envName, test.token)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != test.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, test.wantAuth)
				}
				var requestBody struct {
					Model     string `json:"model"`
					MaxTokens int64  `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request body: %v", err)
				} else if requestBody.Model != "openai-test" || requestBody.MaxTokens != 8192 {
					t.Errorf("model/max_tokens = %q/%d", requestBody.Model, requestBody.MaxTokens)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"openai-test","choices":[{"index":0,"message":{"role":"assistant","content":"Hello","refusal":null,"annotations":[]},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			}))
			defer server.Close()

			adapter, err := NewOpenAIChatCompletions("openai-test", server.URL+"/v1", test.envName, GenerationSettings{
				MaxOutputTokens: 8192,
			})
			if err != nil {
				t.Fatalf("NewOpenAIChatCompletions() error = %v", err)
			}
			var response *model.LLMResponse
			for item, generateErr := range adapter.GenerateContent(context.Background(), &model.LLMRequest{
				Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
			}, false) {
				if generateErr != nil {
					t.Fatalf("GenerateContent() error = %v", generateErr)
				}
				response = item
			}
			if response == nil || len(response.Content.Parts) != 1 || response.Content.Parts[0].Text != "Hello" {
				t.Fatalf("GenerateContent() response = %#v", response)
			}
		})
	}
}

func TestOpenAIChatCompletionsReportsConfiguredEnvironmentVariableWhenMissing(t *testing.T) {
	const environmentName = "MATERIALMIND_MISSING_OPENAI_TOKEN"
	t.Setenv(environmentName, "")
	_, err := NewOpenAIChatCompletions("openai-test", "", environmentName, GenerationSettings{MaxOutputTokens: 4096})
	if err == nil || !strings.Contains(err.Error(), environmentName) {
		t.Fatalf("NewOpenAIChatCompletions() error = %v", err)
	}
}

func TestOpenAIChatCompletionsListsModels(t *testing.T) {
	const environmentName = "MATERIALMIND_MODEL_LIST_TOKEN"
	t.Setenv(environmentName, "catalog-token")
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer catalog-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"zeta/model","object":"model","created":2,"owned_by":"zeta"},{"id":"alpha/model","object":"model","created":1,"owned_by":"alpha"}]}`)
	}))
	defer server.Close()

	provider, err := NewProvider(ProviderConfig{
		Compatibility:     CompatibilityOpenAIChatCompletions,
		BaseURL:           server.URL + "/v1",
		BearerTokenEnvVar: environmentName,
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	models, err := provider.(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "alpha/model" || models[1].OwnedBy != "zeta" {
		t.Fatalf("ListModels() = %#v", models)
	}
}

func TestToOpenAIChatRequest(t *testing.T) {
	temperature := float32(0.2)
	reasoningEffort := "ultra"
	request, err := toOpenAIChatRequest(&model.LLMRequest{
		Model: "openai-test",
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
		t.Fatalf("toOpenAIChatRequest() error = %v", err)
	}
	if request.Model != "openai-test" || !request.MaxCompletionTokens.Valid() || request.MaxCompletionTokens.Value != 1234 || request.MaxTokens.Valid() {
		t.Fatalf("request model/token limits = %q/%#v/%#v", request.Model, request.MaxCompletionTokens, request.MaxTokens)
	}
	if len(request.Messages) != 4 || len(request.Tools) != 1 {
		t.Fatalf("request shape = messages:%d tools:%d", len(request.Messages), len(request.Tools))
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{"read_file", "tool-1", "Inspect the project", "Be precise", `"role":"tool"`, `"reasoning_effort":"ultra"`, `"max_completion_tokens":1234`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
	for _, removed := range []string{`"temperature"`, `"top_p"`, `"top_k"`, `"stop"`} {
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

func TestToOpenAIChatRequestInlinesTextAttachmentAsText(t *testing.T) {
	attachment := genai.NewPartFromBytes([]byte("package example"), "text/plain")
	attachment.InlineData.DisplayName = "context.go"
	request, err := toOpenAIChatRequest(&model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				{Text: "Review the attachment"},
				attachment,
			}, genai.RoleUser),
		},
	}, "openai-test", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("toOpenAIChatRequest() error = %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{
		`"type":"text"`,
		`"text":"Attached file: context.go\npackage example"`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
	for _, removed := range []string{`"type":"file"`, "file_data"} {
		if strings.Contains(string(encoded), removed) {
			t.Fatalf("request JSON contains removed part %q: %s", removed, encoded)
		}
	}
}

func TestToOpenAIChatRequestSendsPDFAsFile(t *testing.T) {
	pdfContent := []byte("%PDF-1.4 fake")
	attachment := genai.NewPartFromBytes(pdfContent, "application/pdf")
	attachment.InlineData.DisplayName = "doc.pdf"
	request, err := toOpenAIChatRequest(&model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				{Text: "Review the attachment"},
				attachment,
			}, genai.RoleUser),
		},
	}, "openai-test", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("toOpenAIChatRequest() error = %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{
		`"type":"file"`,
		`"filename":"doc.pdf"`,
		`"file_data":"` + base64.StdEncoding.EncodeToString(pdfContent) + `"`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
}

func TestOpenAIAdaptersRejectUnsupportedReasoningEffort(t *testing.T) {
	unsupported := "extreme"
	tests := []struct {
		name   string
		create func() error
	}{
		{
			name: "Chat Completions",
			create: func() error {
				_, err := NewOpenAIChatCompletions("openai-test", "", "", GenerationSettings{
					MaxOutputTokens: 4096,
					ReasoningEffort: &unsupported,
				})
				return err
			},
		},
		{
			name: "Responses",
			create: func() error {
				_, err := NewOpenAIResponses("openai-test", "", "", GenerationSettings{
					MaxOutputTokens: 4096,
					ReasoningEffort: &unsupported,
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.create(); err == nil || !strings.Contains(err.Error(), unsupported) {
				t.Fatalf("constructor error = %v", err)
			}
		})
	}
}

func TestToOpenAIChatRequestMergesAdjacentTextRoles(t *testing.T) {
	request, err := toOpenAIChatRequest(&model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("First failed prompt", genai.RoleUser),
			genai.NewContentFromText("Retry prompt", genai.RoleUser),
			genai.NewContentFromText("First answer", genai.RoleModel),
			genai.NewContentFromText("Second answer", genai.RoleModel),
		},
	}, "openai-test", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("toOpenAIChatRequest() error = %v", err)
	}
	if len(request.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(request.Messages))
	}
	user := request.Messages[0].OfUser
	if user == nil || !user.Content.OfString.Valid() || user.Content.OfString.Value != "First failed prompt\n\nRetry prompt" {
		t.Fatalf("merged user message = %#v", user)
	}
	assistant := request.Messages[1].OfAssistant
	if assistant == nil || !assistant.Content.OfString.Valid() || assistant.Content.OfString.Value != "First answer\n\nSecond answer" {
		t.Fatalf("merged assistant message = %#v", assistant)
	}
}

func TestFromOpenAIChatCompletion(t *testing.T) {
	var completion openai.ChatCompletion
	if err := json.Unmarshal([]byte(`{
		"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"openai-test",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Checking.","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"list_directory","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls","logprobs":null}],
		"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
	}`), &completion); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	response, err := fromOpenAIChatCompletion(&completion, false, true)
	if err != nil {
		t.Fatalf("fromOpenAIChatCompletion() error = %v", err)
	}
	if len(response.Content.Parts) != 2 || response.Content.Parts[1].FunctionCall.Name != "list_directory" {
		t.Fatalf("response parts = %#v", response.Content.Parts)
	}
	if response.UsageMetadata.TotalTokenCount != 14 || !response.TurnComplete {
		t.Fatalf("response metadata = %#v", response)
	}
}

func TestFromOpenAIChatCompletionPreservesMalformedFunctionArguments(t *testing.T) {
	var completion openai.ChatCompletion
	if err := json.Unmarshal([]byte(`{
		"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"openai-test",
		"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"edit_file","arguments":"{\"patch\":"}}]},"finish_reason":"tool_calls","logprobs":null}],
		"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
	}`), &completion); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	response, err := fromOpenAIChatCompletion(&completion, false, true)
	if err != nil {
		t.Fatalf("fromOpenAIChatCompletion() error = %v", err)
	}
	call := response.Content.Parts[0].FunctionCall
	decodeError, malformed := FunctionArgumentsDecodeError(call.Args)
	if !malformed || !strings.Contains(decodeError, "unexpected end of JSON input") {
		t.Fatalf("decode error = %q, malformed = %t", decodeError, malformed)
	}
}

func TestOpenAIChatCompletionsStreamsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter, err := NewOpenAIChatCompletions("openai-test", server.URL, "", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("NewOpenAIChatCompletions() error = %v", err)
	}
	var partials []string
	var final *model.LLMResponse
	for response, generateErr := range adapter.GenerateContent(context.Background(), &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
	}, true) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
		if response.Partial {
			partials = append(partials, response.Content.Parts[0].Text)
		} else {
			final = response
		}
	}
	if strings.Join(partials, "") != "Hello" {
		t.Fatalf("partial text = %q", strings.Join(partials, ""))
	}
	if final == nil || len(final.Content.Parts) != 1 || final.Content.Parts[0].Text != "Hello" || !final.TurnComplete {
		t.Fatalf("final response = %#v", final)
	}
}

func TestOpenAIChatCompletionsAccumulatesStreamedFunctionCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"openai-test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\""}}]},"finish_reason":null}]}

`)
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"openai-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"go.mod\"}"}}]},"finish_reason":null}]}

`)
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"openai-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	}))
	defer server.Close()

	adapter, err := NewOpenAIChatCompletions("openai-test", server.URL, "", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("NewOpenAIChatCompletions() error = %v", err)
	}
	var final *model.LLMResponse
	for response, generateErr := range adapter.GenerateContent(context.Background(), &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("Read go.mod", genai.RoleUser)},
	}, true) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
		if !response.Partial {
			final = response
		}
	}
	if final == nil || len(final.Content.Parts) != 1 || final.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("final response = %#v", final)
	}
	call := final.Content.Parts[0].FunctionCall
	if call.ID != "tool-1" || call.Name != "read_file" || call.Args["path"] != "go.mod" {
		t.Fatalf("function call = %#v", call)
	}
}
