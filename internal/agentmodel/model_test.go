package agentmodel

import (
	"reflect"
	"testing"

	"google.golang.org/genai"
)

func TestNewSelectsProviderImplementation(t *testing.T) {
	tests := []struct {
		compatibility string
		baseURL       string
		wantType      any
	}{
		{compatibility: "anthropic-compatible", wantType: &Anthropic{}},
		{
			compatibility: "google-gemini",
			baseURL:       "https://gemini.example.test",
			wantType:      &Gemini{},
		},
		{compatibility: "openai-chat-completions", wantType: &OpenAIChatCompletions{}},
		{compatibility: "responses", wantType: &OpenAIResponses{}},
	}
	for _, test := range tests {
		t.Run(test.compatibility, func(t *testing.T) {
			adapter, err := New(Config{
				Compatibility: test.compatibility,
				Model:         "test-model",
				BaseURL:       test.baseURL,
				GenerationSettings: GenerationSettings{
					MaxOutputTokens: 4096,
				},
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			switch test.wantType.(type) {
			case *Anthropic:
				if _, ok := adapter.(*Anthropic); !ok {
					t.Fatalf("New() type = %T, want *Anthropic", adapter)
				}
			case *Gemini:
				if _, ok := adapter.(*Gemini); !ok {
					t.Fatalf("New() type = %T, want *Gemini", adapter)
				}
			case *OpenAIChatCompletions:
				if _, ok := adapter.(*OpenAIChatCompletions); !ok {
					t.Fatalf("New() type = %T, want *OpenAIChatCompletions", adapter)
				}
			case *OpenAIResponses:
				if _, ok := adapter.(*OpenAIResponses); !ok {
					t.Fatalf("New() type = %T, want *OpenAIResponses", adapter)
				}
			}
		})
	}
}

func TestNewRejectsUnsupportedCompatibility(t *testing.T) {
	if Supports("openai-realtime") {
		t.Fatal("Supports(openai-realtime) = true, want false")
	}
	if _, err := New(Config{Compatibility: "openai-realtime", Model: "test-model"}); err == nil {
		t.Fatal("New() error = nil, want unsupported compatibility error")
	}
}

func TestProviderOptionalCapabilities(t *testing.T) {
	openAIProvider, err := NewProvider(ProviderConfig{Compatibility: CompatibilityOpenAIChatCompletions})
	if err != nil {
		t.Fatalf("NewProvider() OpenAI error = %v", err)
	}
	if _, ok := openAIProvider.(ModelLister); !ok {
		t.Fatalf("OpenAI provider type %T does not implement ModelLister", openAIProvider)
	}
	responsesProvider, err := NewProvider(ProviderConfig{Compatibility: CompatibilityOpenAIResponses})
	if err != nil {
		t.Fatalf("NewProvider() Responses error = %v", err)
	}
	if _, ok := responsesProvider.(ModelLister); !ok {
		t.Fatalf("Responses provider type %T does not implement ModelLister", responsesProvider)
	}

	anthropicProvider, err := NewProvider(ProviderConfig{Compatibility: CompatibilityAnthropic})
	if err != nil {
		t.Fatalf("NewProvider() Anthropic error = %v", err)
	}
	if _, ok := anthropicProvider.(ModelLister); !ok {
		t.Fatalf("Anthropic provider type %T does not implement ModelLister", anthropicProvider)
	}
	geminiProvider, err := NewProvider(ProviderConfig{
		Compatibility: CompatibilityGemini,
		BaseURL:       "https://gemini.example.test",
	})
	if err != nil {
		t.Fatalf("NewProvider() Gemini error = %v", err)
	}
	if _, ok := geminiProvider.(ModelLister); !ok {
		t.Fatalf("Gemini provider type %T does not implement ModelLister", geminiProvider)
	}
}

func TestToolJSONSchemaNormalizesGenAITypeEnums(t *testing.T) {
	schema, err := toolJSONSchema(taskAgentToolDeclaration("workspace_explorer"))
	if err != nil {
		t.Fatalf("toolJSONSchema() error = %v", err)
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"request": map[string]any{
				"type":        "string",
				"description": "Detailed instructions for the sub-agent.",
			},
		},
		"required": []any{"request"},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Fatalf("toolJSONSchema() = %#v, want %#v", schema, want)
	}
}

func taskAgentToolDeclaration(name string) *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        name,
		Description: "Delegate work to a specialized agent.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"request": {
					Type:        genai.TypeString,
					Description: "Detailed instructions for the sub-agent.",
				},
			},
			Required: []string{"request"},
		},
	}
}

func TestIsTextAttachmentMIME(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		// text/* prefix.
		{mediaType: "text/plain", want: true},
		{mediaType: "text/markdown", want: true},
		{mediaType: "text/x-diff", want: true},
		// Upload-allow-listed application types.
		{mediaType: "application/json", want: true},
		{mediaType: "application/javascript", want: true},
		{mediaType: "application/sql", want: true},
		{mediaType: "application/toml", want: true},
		{mediaType: "application/xml", want: true},
		{mediaType: "application/yaml", want: true},
		{mediaType: "application/x-javascript", want: true},
		{mediaType: "application/x-sh", want: true},
		{mediaType: "application/x-yaml", want: true},
		{mediaType: "application/vnd.api+json", want: true},
		{mediaType: "application/ld+xml", want: true},
		// Binaries and other types stay as file parts.
		{mediaType: "application/pdf", want: false},
		{mediaType: "application/octet-stream", want: false},
		{mediaType: "application/zip", want: false},
		{mediaType: "image/png", want: false},
		{mediaType: "audio/mp3", want: false},
		{mediaType: "", want: false},
		{mediaType: "application/", want: false},
	}
	for _, test := range tests {
		name := test.mediaType
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := isTextAttachmentMIME(test.mediaType); got != test.want {
				t.Fatalf("isTextAttachmentMIME(%q) = %v, want %v", test.mediaType, got, test.want)
			}
		})
	}
}
