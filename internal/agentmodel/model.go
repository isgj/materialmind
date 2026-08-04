package agentmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const (
	CompatibilityAnthropic             = "anthropic"
	CompatibilityGemini                = "gemini"
	CompatibilityOpenAIChatCompletions = "openai-chat-completions"
	CompatibilityOpenAIResponses       = "openai-responses"

	defaultMaxOutputTokens = 4096
)

type GenerationSettings struct {
	ContextWindowTokens int64
	MaxOutputTokens     int64
	ReasoningEffort     *string
}

type Config struct {
	Compatibility     string
	Model             string
	BaseURL           string
	BearerTokenEnvVar string
	BearerToken       string
	CredentialScope   string
	GenerationSettings
}

type ProviderConfig struct {
	Compatibility     string
	BaseURL           string
	BearerTokenEnvVar string
	BearerToken       string
	CredentialScope   string
}

type Provider interface {
	NewModel(modelName string, generationSettings GenerationSettings) (model.LLM, error)
}

type AvailableModel struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"displayName,omitempty"`
	OwnedBy             string `json:"ownedBy,omitempty"`
	ContextWindowTokens int64  `json:"contextWindowTokens,omitempty"`
	MaxOutputTokens     int64  `json:"maxOutputTokens,omitempty"`
}

// ModelLister is an optional provider capability.
type ModelLister interface {
	ListModels(ctx context.Context) ([]AvailableModel, error)
}

// New creates the ADK model implementation selected by the provider's API
// compatibility setting.
func New(config Config) (model.LLM, error) {
	provider, err := NewProvider(ProviderConfig{
		Compatibility:     config.Compatibility,
		BaseURL:           config.BaseURL,
		BearerTokenEnvVar: config.BearerTokenEnvVar,
		BearerToken:       config.BearerToken,
		CredentialScope:   config.CredentialScope,
	})
	if err != nil {
		return nil, err
	}
	return provider.NewModel(config.Model, config.GenerationSettings)
}

func NewProvider(config ProviderConfig) (Provider, error) {
	switch normalizeCompatibility(config.Compatibility) {
	case CompatibilityAnthropic:
		return &anthropicProvider{
			baseURL: config.BaseURL, bearerTokenEnvVar: config.BearerTokenEnvVar,
			bearerToken: config.BearerToken,
		}, nil
	case CompatibilityGemini:
		return &geminiProvider{
			baseURL: config.BaseURL, apiKeyEnvVar: config.BearerTokenEnvVar,
			apiKey: config.BearerToken,
		}, nil
	case CompatibilityOpenAIChatCompletions:
		return &openAIChatCompletionsProvider{
			baseURL: config.BaseURL, bearerTokenEnvVar: config.BearerTokenEnvVar,
			bearerToken: config.BearerToken,
		}, nil
	case CompatibilityOpenAIResponses:
		return &openAIResponsesProvider{
			baseURL: config.BaseURL, bearerTokenEnvVar: config.BearerTokenEnvVar,
			bearerToken: config.BearerToken, credentialScope: config.CredentialScope,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported API compatibility %q", config.Compatibility)
	}
}

type anthropicProvider struct {
	baseURL           string
	bearerTokenEnvVar string
	bearerToken       string
}

func (p *anthropicProvider) NewModel(
	modelName string,
	generationSettings GenerationSettings,
) (model.LLM, error) {
	return newAnthropic(
		modelName,
		p.baseURL,
		p.bearerTokenEnvVar,
		p.bearerToken,
		generationSettings,
	)
}

type openAIChatCompletionsProvider struct {
	baseURL           string
	bearerTokenEnvVar string
	bearerToken       string
}

type openAIResponsesProvider struct {
	baseURL           string
	bearerTokenEnvVar string
	bearerToken       string
	credentialScope   string
}

func (p *openAIResponsesProvider) NewModel(
	modelName string,
	generationSettings GenerationSettings,
) (model.LLM, error) {
	return newOpenAIResponses(
		modelName,
		p.baseURL,
		p.bearerTokenEnvVar,
		p.bearerToken,
		p.credentialScope,
		generationSettings,
	)
}

func (p *openAIChatCompletionsProvider) NewModel(
	modelName string,
	generationSettings GenerationSettings,
) (model.LLM, error) {
	return newOpenAIChatCompletions(
		modelName,
		p.baseURL,
		p.bearerTokenEnvVar,
		p.bearerToken,
		generationSettings,
	)
}

func Supports(compatibility string) bool {
	return normalizeCompatibility(compatibility) != ""
}

func normalizeCompatibility(compatibility string) string {
	switch strings.ToLower(strings.TrimSpace(compatibility)) {
	case "anthropic", "claude", "claude-compatible", "anthropic-compatible":
		return CompatibilityAnthropic
	case "gemini", "google-gemini", "gemini-api":
		return CompatibilityGemini
	case "openai-chat-completions", "openai-chat", "chat-completions":
		return CompatibilityOpenAIChatCompletions
	case "openai-responses", "openai-response", "responses":
		return CompatibilityOpenAIResponses
	default:
		return ""
	}
}

func toolJSONSchema(declaration *genai.FunctionDeclaration) (map[string]any, error) {
	value := declaration.ParametersJsonSchema
	if value == nil {
		value = declaration.Parameters
	}
	if value == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	return normalizeJSONSchema(schema).(map[string]any), nil
}

func normalizeJSONSchema(value any) any {
	switch current := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(current))
		for key, child := range current {
			if key == "type" {
				normalized[key] = normalizeJSONSchemaType(child)
				continue
			}
			normalized[key] = normalizeJSONSchema(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(current))
		for index, child := range current {
			normalized[index] = normalizeJSONSchema(child)
		}
		return normalized
	default:
		return value
	}
}

func normalizeJSONSchemaType(value any) any {
	switch current := value.(type) {
	case string:
		switch strings.ToUpper(current) {
		case "STRING", "NUMBER", "INTEGER", "BOOLEAN", "ARRAY", "OBJECT", "NULL":
			return strings.ToLower(current)
		default:
			return current
		}
	case []any:
		normalized := make([]any, len(current))
		for index, child := range current {
			normalized[index] = normalizeJSONSchemaType(child)
		}
		return normalized
	default:
		return value
	}
}
