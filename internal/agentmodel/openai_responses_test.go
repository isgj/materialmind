package agentmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openairesponses "github.com/openai/openai-go/v3/responses"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestOpenAIResponsesUsesConfiguredEndpointAndBearerToken(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	tests := []struct {
		name     string
		envName  string
		token    string
		wantAuth string
	}{
		{name: "configured authentication", envName: "MATERIALMIND_RESPONSES_TOKEN", token: "configured-token", wantAuth: "Bearer configured-token"},
		{name: "authentication omitted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.envName != "" {
				t.Setenv(test.envName, test.token)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					t.Errorf("request = %s %s, want POST /v1/responses", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != test.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, test.wantAuth)
				}
				var requestBody map[string]any
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request body: %v", err)
				} else {
					if requestBody["model"] != "responses-test" || requestBody["max_output_tokens"] != float64(8192) {
						t.Errorf("model/max_output_tokens = %#v/%#v", requestBody["model"], requestBody["max_output_tokens"])
					}
					if requestBody["store"] != false {
						t.Errorf("generation settings = %#v", requestBody)
					}
					for _, removed := range []string{"temperature", "top_p", "top_k", "stop"} {
						if _, ok := requestBody[removed]; ok {
							t.Errorf("request contains removed setting %q: %#v", removed, requestBody)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, openAIResponsesJSON("Hello"))
			}))
			defer server.Close()

			adapter, err := NewOpenAIResponses("responses-test", server.URL+"/v1", test.envName, GenerationSettings{
				MaxOutputTokens: 8192,
			})
			if err != nil {
				t.Fatalf("NewOpenAIResponses() error = %v", err)
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
			if response == nil || responseText(response.Content) != "Hello" {
				t.Fatalf("GenerateContent() response = %#v", response)
			}
		})
	}
}

func TestFilterPreservedResponseFunctionCalls(t *testing.T) {
	preserved := openAIResponsesContext{
		Scope: "test-scope",
		Output: []json.RawMessage{
			json.RawMessage(`{"type":"reasoning","id":"reasoning-1"}`),
			json.RawMessage(`{"type":"function_call","call_id":"ordinary-1","name":"grep"}`),
			json.RawMessage(`{"type":"function_call","call_id":"delegation-1","name":"workspace_explorer"}`),
		},
	}
	encoded, err := json.Marshal(preserved)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	content := &genai.Content{Parts: []*genai.Part{{
		Thought: true,
		ThoughtSignature: append(
			[]byte(openAIResponsesContextPrefix),
			encoded...,
		),
	}}}

	if err := FilterPreservedResponseFunctionCalls(
		content,
		map[string]struct{}{"delegation-1": {}},
	); err != nil {
		t.Fatalf("FilterPreservedResponseFunctionCalls() error = %v", err)
	}

	signature := string(content.Parts[0].ThoughtSignature)
	var filtered openAIResponsesContext
	if err := json.Unmarshal(
		[]byte(strings.TrimPrefix(signature, openAIResponsesContextPrefix)),
		&filtered,
	); err != nil {
		t.Fatalf("decode filtered context: %v", err)
	}
	if len(filtered.Output) != 2 {
		t.Fatalf("filtered output count = %d, want 2", len(filtered.Output))
	}
	for _, raw := range filtered.Output {
		if strings.Contains(string(raw), "delegation-1") {
			t.Fatalf("filtered output still contains deferred call: %s", raw)
		}
	}
}

func TestOpenAIResponsesReportsConfiguredEnvironmentVariableWhenMissing(t *testing.T) {
	const environmentName = "MATERIALMIND_MISSING_RESPONSES_TOKEN"
	t.Setenv(environmentName, "")
	_, err := NewOpenAIResponses("responses-test", "", environmentName, GenerationSettings{MaxOutputTokens: 4096})
	if err == nil || !strings.Contains(err.Error(), environmentName) {
		t.Fatalf("NewOpenAIResponses() error = %v", err)
	}
}

func TestOpenAIResponsesListsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-z","object":"model","created":2,"owned_by":"openai"},{"id":"gpt-a","object":"model","created":1,"owned_by":"openai"}]}`)
	}))
	defer server.Close()

	provider, err := NewProvider(ProviderConfig{
		Compatibility: CompatibilityOpenAIResponses,
		BaseURL:       server.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	models, err := provider.(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "gpt-a" || models[1].ID != "gpt-z" {
		t.Fatalf("ListModels() = %#v", models)
	}
}

func TestToOpenAIResponsesRequest(t *testing.T) {
	temperature := float32(0.2)
	reasoningEffort := "ultra"
	request, err := toOpenAIResponsesRequest(&model.LLMRequest{
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
	}, "responses-test", GenerationSettings{
		MaxOutputTokens: 4096,
		ReasoningEffort: &reasoningEffort,
	}, "scope-1")
	if err != nil {
		t.Fatalf("toOpenAIResponsesRequest() error = %v", err)
	}
	if request.Model != "responses-test" || !request.MaxOutputTokens.Valid() || request.MaxOutputTokens.Value != 1234 {
		t.Fatalf("request model/max output tokens = %q/%#v", request.Model, request.MaxOutputTokens)
	}
	if len(request.Input.OfInputItemList) != 3 || len(request.Tools) != 1 || !request.Store.Valid() || request.Store.Value {
		t.Fatalf("request shape = input:%d tools:%d store:%#v", len(request.Input.OfInputItemList), len(request.Tools), request.Store)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{"read_file", "tool-1", "Inspect the project", "Be precise", `"type":"function_call_output"`, `"strict":false`, `"reasoning":{"effort":"ultra","summary":"auto"`, "reasoning.encrypted_content"} {
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

func TestToOpenAIResponsesRequestIncludesAttachment(t *testing.T) {
	attachment := genai.NewPartFromBytes([]byte("package example"), "text/plain")
	attachment.InlineData.DisplayName = "context.go"
	request, err := toOpenAIResponsesRequest(&model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				{Text: "Review the attachment"},
				attachment,
			}, genai.RoleUser),
		},
	}, "responses-test", GenerationSettings{MaxOutputTokens: 4096}, "scope-1")
	if err != nil {
		t.Fatalf("toOpenAIResponsesRequest() error = %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, expected := range []string{
		`"type":"input_file"`,
		`"filename":"context.go"`,
		`"file_data":"cGFja2FnZSBleGFtcGxl"`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("request JSON does not contain %q: %s", expected, encoded)
		}
	}
}

func TestOpenAIResponsesPreservesOutputItemsForTheNextTurn(t *testing.T) {
	var response openairesponses.Response
	if err := json.Unmarshal([]byte(`{
		"id":"resp-1","object":"response","created_at":1,"status":"completed","model":"responses-test",
		"output":[
			{"id":"rs-1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"Inspecting the project."}],"encrypted_content":"encrypted-reasoning"},
			{"id":"msg-1","type":"message","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"Checking.","annotations":[]}]},
			{"id":"fc-1","type":"function_call","status":"completed","call_id":"call-1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"}
		],
		"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3},"output_tokens":10,"output_tokens_details":{"reasoning_tokens":6},"total_tokens":20}
	}`), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	converted, err := fromOpenAIResponsesResponse(&response, "scope-1")
	if err != nil {
		t.Fatalf("fromOpenAIResponsesResponse() error = %v", err)
	}
	if len(converted.Content.Parts) != 4 || !converted.Content.Parts[0].Thought || len(converted.Content.Parts[0].ThoughtSignature) == 0 {
		t.Fatalf("response parts = %#v", converted.Content.Parts)
	}
	if thought := converted.Content.Parts[1]; !thought.Thought || thought.Text != "Inspecting the project." {
		t.Fatalf("reasoning summary part = %#v", thought)
	}
	call := converted.Content.Parts[3].FunctionCall
	if call == nil || call.ID != "call-1" || call.Name != "read_file" || call.Args["path"] != "go.mod" {
		t.Fatalf("function call = %#v", call)
	}
	if converted.UsageMetadata.CandidatesTokenCount != 4 || converted.UsageMetadata.ThoughtsTokenCount != 6 || converted.UsageMetadata.CachedContentTokenCount != 3 {
		t.Fatalf("usage = %#v", converted.UsageMetadata)
	}

	nextRequest, err := toOpenAIResponsesRequest(&model.LLMRequest{Contents: []*genai.Content{
		genai.NewContentFromText("Inspect the project", genai.RoleUser),
		converted.Content,
		genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "call-1", Name: "read_file", Response: map[string]any{"content": "module test"},
		}}}, genai.RoleUser),
	}}, "responses-test", GenerationSettings{MaxOutputTokens: 4096}, "scope-1")
	if err != nil {
		t.Fatalf("toOpenAIResponsesRequest() next turn error = %v", err)
	}
	encoded, err := json.Marshal(nextRequest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	requestJSON := string(encoded)
	for _, expected := range []string{"encrypted-reasoning", `"id":"msg-1"`, `"phase":"commentary"`, `"type":"function_call_output"`} {
		if !strings.Contains(requestJSON, expected) {
			t.Fatalf("next request does not contain %q: %s", expected, requestJSON)
		}
	}
	if strings.Count(requestJSON, "encrypted-reasoning") != 1 {
		t.Fatalf("preserved reasoning count = %d, request = %s", strings.Count(requestJSON, "encrypted-reasoning"), requestJSON)
	}

	differentProviderRequest, err := toOpenAIResponsesRequest(&model.LLMRequest{Contents: []*genai.Content{
		converted.Content,
	}}, "responses-test", GenerationSettings{MaxOutputTokens: 4096}, "different-scope")
	if err != nil {
		t.Fatalf("toOpenAIResponsesRequest() different scope error = %v", err)
	}
	differentJSON, err := json.Marshal(differentProviderRequest)
	if err != nil {
		t.Fatalf("json.Marshal() different scope error = %v", err)
	}
	if strings.Contains(string(differentJSON), "encrypted-reasoning") || !strings.Contains(string(differentJSON), "Checking.") {
		t.Fatalf("different provider request reused scoped context: %s", differentJSON)
	}
}

func TestOpenAIResponsesPreservesMalformedFunctionArguments(t *testing.T) {
	var response openairesponses.Response
	if err := json.Unmarshal([]byte(`{
		"id":"resp-1","object":"response","created_at":1,"status":"completed","model":"responses-test",
		"output":[
			{"id":"fc-1","type":"function_call","status":"completed","call_id":"call-1","name":"edit_file","arguments":"{\"patch\":"}
		],
		"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":14}
	}`), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	converted, err := fromOpenAIResponsesResponse(&response, "scope-1")
	if err != nil {
		t.Fatalf("fromOpenAIResponsesResponse() error = %v", err)
	}
	call := converted.Content.Parts[1].FunctionCall
	decodeError, malformed := FunctionArgumentsDecodeError(call.Args)
	if !malformed || !strings.Contains(decodeError, "unexpected end of JSON input") {
		t.Fatalf("decode error = %q, malformed = %t", decodeError, malformed)
	}
}

func TestOpenAIResponsesStreamsTextAndReasoningSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":1,\"item_id\":\"rs-1\",\"output_index\":0,\"summary_index\":0,\"delta\":\"Inspect\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":2,\"item_id\":\"rs-1\",\"output_index\":0,\"summary_index\":0,\"delta\":\"ing\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg-1\",\"output_index\":1,\"content_index\":0,\"delta\":\"Hel\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"item_id\":\"msg-1\",\"output_index\":1,\"content_index\":0,\"delta\":\"lo\"}\n\n")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":%s}\n\n", openAIResponsesJSONWithSummary("Hello", "Inspecting"))
	}))
	defer server.Close()

	adapter, err := NewOpenAIResponses("responses-test", server.URL, "", GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("NewOpenAIResponses() error = %v", err)
	}
	var textPartials []string
	var thoughtPartials []string
	var final *model.LLMResponse
	for response, generateErr := range adapter.GenerateContent(context.Background(), &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
	}, true) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
		if response.Partial {
			part := response.Content.Parts[0]
			if part.Thought {
				thoughtPartials = append(thoughtPartials, part.Text)
			} else {
				textPartials = append(textPartials, part.Text)
			}
		} else {
			final = response
		}
	}
	if strings.Join(thoughtPartials, "") != "Inspecting" {
		t.Fatalf("partial reasoning summary = %q", strings.Join(thoughtPartials, ""))
	}
	if strings.Join(textPartials, "") != "Hello" {
		t.Fatalf("partial text = %q", strings.Join(textPartials, ""))
	}
	if final == nil || responseText(final.Content) != "Hello" ||
		len(final.Content.Parts) != 3 || !final.Content.Parts[1].Thought ||
		final.Content.Parts[1].Text != "Inspecting" || !final.TurnComplete {
		t.Fatalf("final response = %#v", final)
	}
}

func openAIResponsesJSON(text string) string {
	encodedText, _ := json.Marshal(text)
	return fmt.Sprintf(`{"id":"resp-1","object":"response","created_at":1,"status":"completed","model":"responses-test","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":3}}`, encodedText)
}

func openAIResponsesJSONWithSummary(text, summary string) string {
	encodedText, _ := json.Marshal(text)
	encodedSummary, _ := json.Marshal(summary)
	return fmt.Sprintf(`{"id":"resp-1","object":"response","created_at":1,"status":"completed","model":"responses-test","output":[{"id":"rs-1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":%s}]},{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":5}}`, encodedSummary, encodedText)
}

func responseText(content *genai.Content) string {
	var result strings.Builder
	for _, part := range contentParts(content) {
		if part != nil && part.Text != "" && !part.Thought {
			result.WriteString(part.Text)
		}
	}
	return result.String()
}
