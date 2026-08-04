package agentmodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

var _ model.LLM = (*OpenAIChatCompletions)(nil)

// OpenAIChatCompletions adapts OpenAI-compatible Chat Completions APIs to ADK.
type OpenAIChatCompletions struct {
	completions        openai.ChatCompletionService
	model              string
	generationSettings GenerationSettings
}

func NewOpenAIChatCompletions(
	modelName, baseURL, bearerTokenEnvVar string,
	generationSettings GenerationSettings,
) (*OpenAIChatCompletions, error) {
	return newOpenAIChatCompletions(
		modelName,
		baseURL,
		bearerTokenEnvVar,
		"",
		generationSettings,
	)
}

func newOpenAIChatCompletions(
	modelName, baseURL, bearerTokenEnvVar, bearerToken string,
	generationSettings GenerationSettings,
) (*OpenAIChatCompletions, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	requestOptions, err := openAIRequestOptions(baseURL, bearerTokenEnvVar, bearerToken)
	if err != nil {
		return nil, err
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	if _, err := openAIReasoningEffort(generationSettings.ReasoningEffort); err != nil {
		return nil, err
	}

	return &OpenAIChatCompletions{
		completions:        openai.NewChatCompletionService(requestOptions...),
		model:              modelName,
		generationSettings: generationSettings,
	}, nil
}

func (p *openAIChatCompletionsProvider) ListModels(ctx context.Context) ([]AvailableModel, error) {
	return listOpenAIModels(ctx, p.baseURL, p.bearerTokenEnvVar, p.bearerToken)
}

func listOpenAIModels(
	ctx context.Context,
	baseURL, bearerTokenEnvVar, bearerToken string,
) ([]AvailableModel, error) {
	requestOptions, err := openAIRequestOptions(baseURL, bearerTokenEnvVar, bearerToken)
	if err != nil {
		return nil, err
	}
	models := openai.NewModelService(requestOptions...)
	page, err := models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai models request: %w", err)
	}
	result := make([]AvailableModel, 0, len(page.Data))
	for _, item := range page.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			result = append(result, AvailableModel{ID: id, OwnedBy: item.OwnedBy})
		}
	}
	slices.SortFunc(result, func(first, second AvailableModel) int {
		return strings.Compare(strings.ToLower(first.ID), strings.ToLower(second.ID))
	})
	return result, nil
}

func openAIRequestOptions(
	baseURL, bearerTokenEnvVar, bearerToken string,
) ([]option.RequestOption, error) {
	requestOptions := []option.RequestOption{option.WithEnvironmentProduction()}
	if baseURL = strings.TrimSpace(baseURL); baseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(baseURL))
	}
	if bearerToken = strings.TrimSpace(bearerToken); bearerToken != "" {
		requestOptions = append(requestOptions, option.WithAPIKey(bearerToken))
	} else if bearerTokenEnvVar = strings.TrimSpace(bearerTokenEnvVar); bearerTokenEnvVar != "" {
		environmentToken := strings.TrimSpace(os.Getenv(bearerTokenEnvVar))
		if environmentToken == "" {
			return nil, fmt.Errorf("bearer token environment variable %q is not set", bearerTokenEnvVar)
		}
		requestOptions = append(requestOptions, option.WithAPIKey(environmentToken))
	}
	return requestOptions, nil
}

func (m *OpenAIChatCompletions) Name() string { return m.model }

func (m *OpenAIChatCompletions) GenerateContent(
	ctx context.Context,
	req *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		params, err := toOpenAIChatRequest(req, m.model, m.generationSettings)
		if err != nil {
			yield(nil, err)
			return
		}
		if !stream {
			completion, err := m.completions.New(ctx, params)
			if err != nil {
				yield(nil, fmt.Errorf("openai chat completions request: %w", err))
				return
			}
			response, err := fromOpenAIChatCompletion(completion, false, true)
			yield(response, err)
			return
		}

		completionStream := m.completions.NewStreaming(ctx, params)
		defer completionStream.Close()
		var accumulated openai.ChatCompletionAccumulator
		for completionStream.Next() {
			chunk := completionStream.Current()
			if !accumulated.AddChunk(chunk) {
				yield(nil, fmt.Errorf("accumulate openai chat completions stream: inconsistent chunk"))
				return
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content == "" {
					continue
				}
				if !yield(&model.LLMResponse{
					Content:      genai.NewContentFromText(choice.Delta.Content, genai.RoleModel),
					ModelVersion: accumulated.Model,
					Partial:      true,
				}, nil) {
					return
				}
			}
		}
		if err := completionStream.Err(); err != nil {
			yield(nil, fmt.Errorf("openai chat completions stream: %w", err))
			return
		}
		response, err := fromOpenAIChatCompletion(&accumulated.ChatCompletion, false, true)
		yield(response, err)
	}
}

func toOpenAIChatRequest(
	req *model.LLMRequest,
	fallbackModel string,
	generationSettings GenerationSettings,
) (openai.ChatCompletionNewParams, error) {
	if req == nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("LLM request is required")
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = fallbackModel
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	reasoningEffort, err := openAIReasoningEffort(generationSettings.ReasoningEffort)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}
	params := openai.ChatCompletionNewParams{
		Model:           shared.ChatModel(modelName),
		ReasoningEffort: reasoningEffort,
	}
	setOpenAIChatMaxOutputTokens(&params, generationSettings.MaxOutputTokens, reasoningEffort != "")

	if req.Config != nil {
		if req.Config.MaxOutputTokens > 0 {
			setOpenAIChatMaxOutputTokens(&params, int64(req.Config.MaxOutputTokens), reasoningEffort != "")
		}
		if systemInstruction := contentText(req.Config.SystemInstruction); systemInstruction != "" {
			params.Messages = append(params.Messages, openai.SystemMessage(systemInstruction))
		}
		tools, err := toOpenAIChatTools(req.Config.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}
		params.Tools = tools
	}

	for _, content := range req.Contents {
		messages, err := toOpenAIChatMessages(content)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}
		for _, message := range messages {
			params.Messages = appendOpenAIChatMessage(params.Messages, message)
		}
	}
	if len(params.Messages) == 0 {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("at least one message is required")
	}
	return params, nil
}

func setOpenAIChatMaxOutputTokens(params *openai.ChatCompletionNewParams, value int64, reasoning bool) {
	if reasoning {
		params.MaxCompletionTokens = param.NewOpt(value)
		params.MaxTokens = param.Opt[int64]{}
		return
	}
	params.MaxTokens = param.NewOpt(value)
	params.MaxCompletionTokens = param.Opt[int64]{}
}

func appendOpenAIChatMessage(
	messages []openai.ChatCompletionMessageParamUnion,
	next openai.ChatCompletionMessageParamUnion,
) []openai.ChatCompletionMessageParamUnion {
	if len(messages) == 0 {
		return append(messages, next)
	}
	previous := &messages[len(messages)-1]
	if previous.OfUser != nil && next.OfUser != nil &&
		previous.OfUser.Content.OfString.Valid() && next.OfUser.Content.OfString.Valid() {
		previous.OfUser.Content.OfString = param.NewOpt(joinOpenAIChatText(
			previous.OfUser.Content.OfString.Value,
			next.OfUser.Content.OfString.Value,
		))
		return messages
	}
	if previous.OfAssistant != nil && next.OfAssistant != nil {
		if next.OfAssistant.Content.OfString.Valid() {
			if previous.OfAssistant.Content.OfString.Valid() {
				previous.OfAssistant.Content.OfString = param.NewOpt(joinOpenAIChatText(
					previous.OfAssistant.Content.OfString.Value,
					next.OfAssistant.Content.OfString.Value,
				))
			} else {
				previous.OfAssistant.Content.OfString = next.OfAssistant.Content.OfString
			}
		}
		previous.OfAssistant.ToolCalls = append(previous.OfAssistant.ToolCalls, next.OfAssistant.ToolCalls...)
		return messages
	}
	return append(messages, next)
}

func joinOpenAIChatText(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "\n\n" + second
}

func openAIReasoningEffort(value *string) (shared.ReasoningEffort, error) {
	if value == nil {
		return "", nil
	}
	effort := strings.ToLower(strings.TrimSpace(*value))
	switch shared.ReasoningEffort(effort) {
	case shared.ReasoningEffortNone,
		shared.ReasoningEffortMinimal,
		shared.ReasoningEffortLow,
		shared.ReasoningEffortMedium,
		shared.ReasoningEffortHigh,
		shared.ReasoningEffortXhigh,
		shared.ReasoningEffortMax,
		shared.ReasoningEffort("ultra"):
		return shared.ReasoningEffort(effort), nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("unsupported reasoning effort %q", *value)
	}
}

func toOpenAIChatMessages(content *genai.Content) ([]openai.ChatCompletionMessageParamUnion, error) {
	if content == nil {
		return nil, nil
	}
	if content.Role == genai.RoleModel {
		var assistant openai.ChatCompletionAssistantMessageParam
		var textParts []string
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			switch {
			case part.FunctionCall != nil:
				argumentsValue := part.FunctionCall.Args
				if argumentsValue == nil {
					argumentsValue = map[string]any{}
				}
				arguments, err := json.Marshal(argumentsValue)
				if err != nil {
					return nil, fmt.Errorf("encode function call %q: %w", part.FunctionCall.Name, err)
				}
				assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: part.FunctionCall.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      part.FunctionCall.Name,
							Arguments: string(arguments),
						},
					},
				})
			case part.Text != "":
				textParts = append(textParts, part.Text)
			}
		}
		if len(textParts) > 0 {
			assistant.Content.OfString = param.NewOpt(strings.Join(textParts, ""))
		}
		if len(textParts) == 0 && len(assistant.ToolCalls) == 0 {
			return nil, nil
		}
		return []openai.ChatCompletionMessageParamUnion{{OfAssistant: &assistant}}, nil
	}

	var result []openai.ChatCompletionMessageParamUnion
	var textParts []string
	var contentParts []openai.ChatCompletionContentPartUnionParam
	usesContentParts := false
	appendText := func(text string) {
		if usesContentParts {
			contentParts = append(contentParts, openai.TextContentPart(text))
			return
		}
		textParts = append(textParts, text)
	}
	useContentParts := func() {
		if usesContentParts {
			return
		}
		usesContentParts = true
		for _, text := range textParts {
			contentParts = append(contentParts, openai.TextContentPart(text))
		}
		textParts = nil
	}
	flushUser := func() {
		if usesContentParts {
			if len(contentParts) > 0 {
				result = append(result, openai.UserMessage(contentParts))
			}
		} else if len(textParts) > 0 {
			result = append(result, openai.UserMessage(strings.Join(textParts, "")))
		}
		textParts = nil
		contentParts = nil
		usesContentParts = false
	}
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		switch {
		case part.FunctionResponse != nil:
			flushUser()
			encoded, err := json.Marshal(part.FunctionResponse.Response)
			if err != nil {
				return nil, fmt.Errorf("encode function response %q: %w", part.FunctionResponse.Name, err)
			}
			result = append(result, openai.ToolMessage(string(encoded), part.FunctionResponse.ID))
		case part.InlineData != nil:
			useContentParts()
			contentParts = append(contentParts, openAIChatAttachmentPart(part.InlineData))
		case part.Text != "":
			appendText(part.Text)
		}
	}
	flushUser()
	return result, nil
}

func openAIChatAttachmentPart(
	blob *genai.Blob,
) openai.ChatCompletionContentPartUnionParam {
	encoded := base64.StdEncoding.EncodeToString(blob.Data)
	if strings.HasPrefix(blob.MIMEType, "image/") {
		return openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
			URL:    "data:" + blob.MIMEType + ";base64," + encoded,
			Detail: "auto",
		})
	}
	filename := strings.TrimSpace(blob.DisplayName)
	if filename == "" {
		filename = "attachment"
	}
	return openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
		FileData: param.NewOpt(encoded),
		Filename: param.NewOpt(filename),
	})
}

func toOpenAIChatTools(tools []*genai.Tool) ([]openai.ChatCompletionToolUnionParam, error) {
	var result []openai.ChatCompletionToolUnionParam
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		for _, declaration := range tool.FunctionDeclarations {
			if declaration == nil {
				continue
			}
			schema, err := toolJSONSchema(declaration)
			if err != nil {
				return nil, fmt.Errorf("convert schema for tool %q: %w", declaration.Name, err)
			}
			result = append(result, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        declaration.Name,
				Description: param.NewOpt(declaration.Description),
				Parameters:  shared.FunctionParameters(schema),
			}))
		}
	}
	return result, nil
}

func contentText(content *genai.Content) string {
	var parts []string
	for _, part := range contentParts(content) {
		if part != nil && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "")
}

func fromOpenAIChatCompletion(
	completion *openai.ChatCompletion,
	partial, turnComplete bool,
) (*model.LLMResponse, error) {
	if completion == nil || len(completion.Choices) == 0 {
		return nil, fmt.Errorf("openai chat completions response has no choices")
	}
	choice := completion.Choices[0]
	parts := make([]*genai.Part, 0, len(choice.Message.ToolCalls)+1)
	if choice.Message.Content != "" {
		parts = append(parts, genai.NewPartFromText(choice.Message.Content))
	}
	for _, toolCall := range choice.Message.ToolCalls {
		if toolCall.Type != "function" {
			continue
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   toolCall.ID,
			Name: toolCall.Function.Name,
			Args: decodeFunctionArguments(toolCall.Function.Arguments),
		}})
	}
	return &model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: parts},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(completion.Usage.PromptTokens),
			CandidatesTokenCount: int32(completion.Usage.CompletionTokens),
			TotalTokenCount:      int32(completion.Usage.TotalTokens),
		},
		ModelVersion: completion.Model,
		Partial:      partial,
		TurnComplete: turnComplete,
		FinishReason: openAIChatFinishReason(choice.FinishReason),
	}, nil
}

func openAIChatFinishReason(reason string) genai.FinishReason {
	switch reason {
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	case "", "stop", "tool_calls", "function_call":
		return genai.FinishReasonStop
	default:
		return genai.FinishReasonOther
	}
}
