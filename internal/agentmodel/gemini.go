package agentmodel

import (
	"context"
	"fmt"
	"iter"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"

	"google.golang.org/adk/v2/model"
	adkgemini "google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

var _ model.LLM = (*Gemini)(nil)

const omittedGeminiAPIKey = "materialmind-omitted-api-key"

// Gemini applies MaterialMind's per-model generation settings to ADK's native
// Gemini implementation.
type Gemini struct {
	delegate           model.LLM
	model              string
	generationSettings GenerationSettings
}

type geminiProvider struct {
	baseURL      string
	apiKeyEnvVar string
	apiKey       string
}

func NewGemini(
	modelName, baseURL, apiKeyEnvVar string,
	generationSettings GenerationSettings,
) (*Gemini, error) {
	return newGemini(modelName, baseURL, apiKeyEnvVar, "", generationSettings)
}

func newGemini(
	modelName, baseURL, apiKeyEnvVar, apiKey string,
	generationSettings GenerationSettings,
) (*Gemini, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	if generationSettings.MaxOutputTokens > math.MaxInt32 {
		return nil, fmt.Errorf(
			"Gemini max output tokens must not exceed %d",
			int64(math.MaxInt32),
		)
	}
	if generationSettings.ReasoningEffort != nil &&
		strings.TrimSpace(*generationSettings.ReasoningEffort) != "" {
		return nil, fmt.Errorf(
			"reasoning effort is not supported by Gemini providers; use the provider default",
		)
	}
	clientConfig, err := geminiClientConfig(baseURL, apiKeyEnvVar, apiKey)
	if err != nil {
		return nil, err
	}
	delegate, err := adkgemini.NewModel(context.Background(), modelName, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create Gemini model: %w", err)
	}
	return &Gemini{
		delegate:           delegate,
		model:              modelName,
		generationSettings: generationSettings,
	}, nil
}

func (p *geminiProvider) NewModel(
	modelName string,
	generationSettings GenerationSettings,
) (model.LLM, error) {
	return newGemini(
		modelName,
		p.baseURL,
		p.apiKeyEnvVar,
		p.apiKey,
		generationSettings,
	)
}

func (p *geminiProvider) ListModels(ctx context.Context) ([]AvailableModel, error) {
	clientConfig, err := geminiClientConfig(p.baseURL, p.apiKeyEnvVar, p.apiKey)
	if err != nil {
		return nil, err
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create Gemini client: %w", err)
	}
	result := make([]AvailableModel, 0)
	for item, listErr := range client.Models.All(ctx) {
		if listErr != nil {
			return nil, fmt.Errorf("Gemini models request: %w", listErr)
		}
		if item == nil || !geminiModelSupportsGenerateContent(item) {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(item.Name, "models/"))
		if id == "" {
			continue
		}
		result = append(result, AvailableModel{
			ID:                  id,
			DisplayName:         strings.TrimSpace(item.DisplayName),
			OwnedBy:             "Google",
			ContextWindowTokens: int64(item.InputTokenLimit),
			MaxOutputTokens:     int64(item.OutputTokenLimit),
		})
	}
	slices.SortFunc(result, func(first, second AvailableModel) int {
		return strings.Compare(strings.ToLower(first.ID), strings.ToLower(second.ID))
	})
	return result, nil
}

func geminiModelSupportsGenerateContent(item *genai.Model) bool {
	if len(item.SupportedActions) == 0 {
		return true
	}
	return slices.ContainsFunc(item.SupportedActions, func(action string) bool {
		return strings.EqualFold(strings.TrimSpace(action), "generateContent")
	})
}

func geminiClientConfig(
	baseURL, apiKeyEnvVar, apiKey string,
) (*genai.ClientConfig, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKeyEnvVar = strings.TrimSpace(apiKeyEnvVar)
		if apiKeyEnvVar != "" {
			apiKey = strings.TrimSpace(os.Getenv(apiKeyEnvVar))
			if apiKey == "" {
				return nil, fmt.Errorf(
					"Gemini API key environment variable %q is not set",
					apiKeyEnvVar,
				)
			}
		}
	}
	if apiKey == "" && baseURL == "" {
		return nil, fmt.Errorf("Gemini API key is required when using the default endpoint")
	}
	configAPIKey := apiKey
	if configAPIKey == "" {
		// The SDK requires an API key even when a custom endpoint handles its
		// own authentication. The transport removes this sentinel before send.
		configAPIKey = omittedGeminiAPIKey
	}
	config := &genai.ClientConfig{
		APIKey:  configAPIKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: baseURL,
		},
	}
	if apiKey == "" {
		config.HTTPClient = &http.Client{Transport: withoutGoogleAPIKeyTransport{}}
	}
	return config, nil
}

func (m *Gemini) Name() string {
	return m.model
}

func (m *Gemini) GenerateContent(
	ctx context.Context,
	req *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	if req == nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, fmt.Errorf("LLM request is required"))
		}
	}
	request := *req
	request.Contents = slices.Clone(req.Contents)
	config := genai.GenerateContentConfig{}
	if req.Config != nil {
		config = *req.Config
		if req.Config.HTTPOptions != nil {
			httpOptions := *req.Config.HTTPOptions
			httpOptions.Headers = req.Config.HTTPOptions.Headers.Clone()
			config.HTTPOptions = &httpOptions
		}
	}
	if config.MaxOutputTokens <= 0 {
		config.MaxOutputTokens = int32(m.generationSettings.MaxOutputTokens)
	}
	if config.ThinkingConfig == nil {
		config.ThinkingConfig = &genai.ThinkingConfig{}
	} else {
		thinkingConfig := *config.ThinkingConfig
		config.ThinkingConfig = &thinkingConfig
	}
	config.ThinkingConfig.IncludeThoughts = true
	request.Config = &config
	return m.delegate.GenerateContent(ctx, &request, stream)
}

type withoutGoogleAPIKeyTransport struct {
	base http.RoundTripper
}

func (t withoutGoogleAPIKeyTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Del("x-goog-api-key")
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}
