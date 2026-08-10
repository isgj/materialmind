package agentmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestGeminiUsesConfiguredEndpointAPIKeyAndOutputLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/v1beta/models/gemini-test:generateContent" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gemini-key" {
			t.Errorf("x-goog-api-key = %q, want gemini-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		var requestBody struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				MaxOutputTokens int32 `json:"maxOutputTokens"`
				ThinkingConfig  struct {
					IncludeThoughts bool `json:"includeThoughts"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		} else {
			if requestBody.GenerationConfig.MaxOutputTokens != 8192 {
				t.Errorf(
					"maxOutputTokens = %d, want 8192",
					requestBody.GenerationConfig.MaxOutputTokens,
				)
			}
			if !requestBody.GenerationConfig.ThinkingConfig.IncludeThoughts {
				t.Error("thinkingConfig.includeThoughts = false, want true")
			}
			if len(requestBody.Contents) != 1 ||
				requestBody.Contents[0].Role != genai.RoleUser ||
				len(requestBody.Contents[0].Parts) != 1 ||
				requestBody.Contents[0].Parts[0].Text != "Hi" {
				t.Errorf("contents = %#v", requestBody.Contents)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"candidates":[{
				"content":{"role":"model","parts":[
					{"text":"Inspecting.","thought":true},
					{"text":"Hello"}
				]},
				"finishReason":"STOP"
			}],
			"modelVersion":"gemini-test",
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`)
	}))
	defer server.Close()

	adapter, err := newGemini(
		"gemini-test",
		server.URL,
		"",
		"gemini-key",
		GenerationSettings{MaxOutputTokens: 8192},
	)
	if err != nil {
		t.Fatalf("newGemini() error = %v", err)
	}
	if got := adapter.Name(); got != "gemini-test" {
		t.Fatalf("Name() = %q, want gemini-test", got)
	}
	var response *model.LLMResponse
	for item, generateErr := range adapter.GenerateContent(
		context.Background(),
		&model.LLMRequest{
			Model:    adapter.Name(),
			Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
		},
		false,
	) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
		response = item
	}
	if response == nil ||
		response.Content == nil ||
		len(response.Content.Parts) != 2 ||
		!response.Content.Parts[0].Thought ||
		response.Content.Parts[0].Text != "Inspecting." ||
		response.Content.Parts[1].Thought ||
		response.Content.Parts[1].Text != "Hello" {
		t.Fatalf("GenerateContent() response = %#v", response)
	}
}

func TestGeminiUsesOnlyExplicitlyConfiguredEnvironmentVariable(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "global-key-must-not-be-used")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Errorf("x-goog-api-key = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}]
		}`)
	}))
	defer server.Close()

	adapter, err := NewGemini(
		"gemini-test",
		server.URL,
		"",
		GenerationSettings{MaxOutputTokens: 4096},
	)
	if err != nil {
		t.Fatalf("NewGemini() error = %v", err)
	}
	for _, generateErr := range adapter.GenerateContent(
		context.Background(),
		&model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
		},
		false,
	) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
	}
}

func TestGeminiStreamsResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" ||
			r.URL.Query().Get("alt") != "sse" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {
			"candidates":[{
				"content":{"role":"model","parts":[{"text":"Inspecting.","thought":true}]}
			}],
			"modelVersion":"gemini-test"
		}

data: {
			"candidates":[{
				"content":{"role":"model","parts":[{"text":"Hello"}]},
				"finishReason":"STOP"
			}],
			"modelVersion":"gemini-test"
		}

`)
	}))
	defer server.Close()

	adapter, err := newGemini(
		"gemini-test",
		server.URL,
		"",
		"gemini-key",
		GenerationSettings{MaxOutputTokens: 4096},
	)
	if err != nil {
		t.Fatalf("newGemini() error = %v", err)
	}
	var partialThought, partialText, finalThought, finalText strings.Builder
	for response, generateErr := range adapter.GenerateContent(
		context.Background(),
		&model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("Hi", genai.RoleUser)},
		},
		true,
	) {
		if generateErr != nil {
			t.Fatalf("GenerateContent() error = %v", generateErr)
		}
		if response == nil || response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			switch {
			case response.Partial && part.Thought:
				partialThought.WriteString(part.Text)
			case response.Partial:
				partialText.WriteString(part.Text)
			case part.Thought:
				finalThought.WriteString(part.Text)
			default:
				finalText.WriteString(part.Text)
			}
		}
	}
	if partialThought.String() != "Inspecting." ||
		partialText.String() != "Hello" ||
		finalThought.String() != "Inspecting." ||
		finalText.String() != "Hello" {
		t.Fatalf(
			"stream = partial thought %q text %q, final thought %q text %q",
			partialThought.String(),
			partialText.String(),
			finalThought.String(),
			finalText.String(),
		)
	}
}

func TestGeminiReportsConfiguredEnvironmentVariableWhenMissing(t *testing.T) {
	const environmentName = "MATERIALMIND_MISSING_GEMINI_KEY"
	t.Setenv(environmentName, "")
	_, err := NewGemini(
		"gemini-test",
		"https://gemini.example.test",
		environmentName,
		GenerationSettings{MaxOutputTokens: 4096},
	)
	if err == nil || !strings.Contains(err.Error(), environmentName) {
		t.Fatalf("NewGemini() error = %v", err)
	}
}

func TestGeminiRejectsReasoningEffort(t *testing.T) {
	effort := "high"
	_, err := NewGemini(
		"gemini-test",
		"https://gemini.example.test",
		"",
		GenerationSettings{MaxOutputTokens: 4096, ReasoningEffort: &effort},
	)
	if err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("NewGemini() error = %v", err)
	}
}

func TestGeminiListsGenerateContentModels(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" {
			t.Errorf("request = %s %s, want GET /v1beta/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "catalog-key" {
			t.Errorf("x-goog-api-key = %q, want catalog-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch pageToken := r.URL.Query().Get("pageToken"); pageToken {
		case "":
			fmt.Fprint(w, `{
				"models":[
					{
						"name":"models/gemini-zeta",
						"displayName":"Gemini Zeta",
						"inputTokenLimit":1000000,
						"outputTokenLimit":64000,
						"supportedGenerationMethods":["generateContent","countTokens"]
					},
					{
						"name":"models/text-embedding",
						"displayName":"Embedding",
						"supportedGenerationMethods":["embedContent"]
					}
				],
				"nextPageToken":"next"
			}`)
		case "next":
			fmt.Fprint(w, `{
				"models":[{
					"name":"models/gemini-alpha",
					"displayName":"Gemini Alpha",
					"inputTokenLimit":2000000,
					"outputTokenLimit":128000,
					"supportedGenerationMethods":["generateContent"]
				}]
			}`)
		default:
			t.Errorf("pageToken = %q", pageToken)
		}
	}))
	defer server.Close()

	provider, err := NewProvider(ProviderConfig{
		Compatibility: CompatibilityGemini,
		BaseURL:       server.URL,
		BearerToken:   "catalog-key",
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
	if len(models) != 2 ||
		models[0].ID != "gemini-alpha" ||
		models[0].DisplayName != "Gemini Alpha" ||
		models[0].OwnedBy != "Google" ||
		models[0].ContextWindowTokens != 2_000_000 ||
		models[0].MaxOutputTokens != 128_000 ||
		models[1].ID != "gemini-zeta" {
		t.Fatalf("ListModels() = %#v", models)
	}
}
