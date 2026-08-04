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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

var _ model.LLM = (*Anthropic)(nil)

// Anthropic adapts Anthropic's Messages API, including compatible base URLs,
// to the model interface consumed by ADK.
type Anthropic struct {
	client             anthropic.Client
	model              string
	generationSettings GenerationSettings
}

func NewAnthropic(
	modelName, baseURL, bearerTokenEnvVar string,
	generationSettings GenerationSettings,
) (*Anthropic, error) {
	return newAnthropic(
		modelName,
		baseURL,
		bearerTokenEnvVar,
		"",
		generationSettings,
	)
}

func newAnthropic(
	modelName, baseURL, bearerTokenEnvVar, bearerToken string,
	generationSettings GenerationSettings,
) (*Anthropic, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	requestOptions, err := anthropicRequestOptions(baseURL, bearerTokenEnvVar, bearerToken)
	if err != nil {
		return nil, err
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	if _, err := anthropicReasoningEffort(generationSettings.ReasoningEffort); err != nil {
		return nil, err
	}
	return &Anthropic{
		client:             anthropic.NewClient(requestOptions...),
		model:              modelName,
		generationSettings: generationSettings,
	}, nil
}

func (p *anthropicProvider) ListModels(ctx context.Context) ([]AvailableModel, error) {
	requestOptions, err := anthropicRequestOptions(
		p.baseURL,
		p.bearerTokenEnvVar,
		p.bearerToken,
	)
	if err != nil {
		return nil, err
	}
	client := anthropic.NewClient(requestOptions...)
	pager := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{
		Limit: param.NewOpt[int64](1000),
	})
	var result []AvailableModel
	for pager.Next() {
		item := pager.Current()
		if id := strings.TrimSpace(item.ID); id != "" {
			result = append(result, AvailableModel{
				ID:                  id,
				DisplayName:         strings.TrimSpace(item.DisplayName),
				ContextWindowTokens: item.MaxInputTokens,
				MaxOutputTokens:     item.MaxTokens,
			})
		}
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("anthropic models request: %w", err)
	}
	slices.SortFunc(result, func(first, second AvailableModel) int {
		return strings.Compare(strings.ToLower(first.ID), strings.ToLower(second.ID))
	})
	return result, nil
}

func anthropicRequestOptions(
	baseURL, bearerTokenEnvVar, bearerToken string,
) ([]option.RequestOption, error) {
	requestOptions := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	if baseURL = strings.TrimSpace(baseURL); baseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(baseURL))
	}
	if bearerToken = strings.TrimSpace(bearerToken); bearerToken != "" {
		requestOptions = append(requestOptions, option.WithAuthToken(bearerToken))
	} else if bearerTokenEnvVar = strings.TrimSpace(bearerTokenEnvVar); bearerTokenEnvVar != "" {
		environmentToken := strings.TrimSpace(os.Getenv(bearerTokenEnvVar))
		if environmentToken == "" {
			return nil, fmt.Errorf("bearer token environment variable %q is not set", bearerTokenEnvVar)
		}
		requestOptions = append(requestOptions, option.WithAuthToken(environmentToken))
	}
	return requestOptions, nil
}

func (m *Anthropic) Name() string { return m.model }

func (m *Anthropic) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		params, err := toAnthropicRequest(req, m.model, m.generationSettings)
		if err != nil {
			yield(nil, err)
			return
		}
		if !stream {
			message, err := m.client.Messages.New(ctx, params)
			if err != nil {
				yield(nil, fmt.Errorf("anthropic messages request: %w", err))
				return
			}
			yield(fromAnthropicMessage(message, false, true), nil)
			return
		}

		messageStream := m.client.Messages.NewStreaming(ctx, params)
		defer messageStream.Close()
		var accumulated anthropic.Message
		for messageStream.Next() {
			event := messageStream.Current()
			if err := accumulated.Accumulate(event); err != nil {
				yield(nil, fmt.Errorf("accumulate anthropic stream: %w", err))
				return
			}
			if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				if !yield(&model.LLMResponse{
					Content:      genai.NewContentFromText(event.Delta.Text, genai.RoleModel),
					ModelVersion: string(accumulated.Model),
					Partial:      true,
				}, nil) {
					return
				}
			}
		}
		if err := messageStream.Err(); err != nil {
			yield(nil, fmt.Errorf("anthropic messages stream: %w", err))
			return
		}
		yield(fromAnthropicMessage(&accumulated, false, true), nil)
	}
}

func toAnthropicRequest(
	req *model.LLMRequest,
	fallbackModel string,
	generationSettings GenerationSettings,
) (anthropic.MessageNewParams, error) {
	if req == nil {
		return anthropic.MessageNewParams{}, fmt.Errorf("LLM request is required")
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = fallbackModel
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	params := anthropic.MessageNewParams{
		MaxTokens: generationSettings.MaxOutputTokens,
		Model:     anthropic.Model(modelName),
	}
	reasoningEffort, err := anthropicReasoningEffort(generationSettings.ReasoningEffort)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	params.OutputConfig.Effort = reasoningEffort
	if req.Config != nil {
		if req.Config.MaxOutputTokens > 0 {
			params.MaxTokens = int64(req.Config.MaxOutputTokens)
		}
		for _, part := range contentParts(req.Config.SystemInstruction) {
			if part.Text != "" {
				params.System = append(params.System, anthropic.TextBlockParam{Text: part.Text})
			}
		}
		tools, err := toAnthropicTools(req.Config.Tools)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Tools = tools
	}

	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		blocks, err := toAnthropicBlocks(content.Parts)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		if len(blocks) == 0 {
			continue
		}
		if content.Role == genai.RoleModel {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(blocks...))
		}
	}
	if len(params.Messages) == 0 {
		return anthropic.MessageNewParams{}, fmt.Errorf("at least one message is required")
	}
	return params, nil
}

func anthropicReasoningEffort(value *string) (anthropic.OutputConfigEffort, error) {
	if value == nil {
		return "", nil
	}
	effort := strings.ToLower(strings.TrimSpace(*value))
	switch anthropic.OutputConfigEffort(effort) {
	case anthropic.OutputConfigEffortLow,
		anthropic.OutputConfigEffortMedium,
		anthropic.OutputConfigEffortHigh,
		anthropic.OutputConfigEffortXhigh,
		anthropic.OutputConfigEffortMax:
		return anthropic.OutputConfigEffort(effort), nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("unsupported Anthropic reasoning effort %q", *value)
	}
}

func contentParts(content *genai.Content) []*genai.Part {
	if content == nil {
		return nil
	}
	return content.Parts
}

func toAnthropicBlocks(parts []*genai.Part) ([]anthropic.ContentBlockParamUnion, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		switch {
		case part.FunctionCall != nil:
			blocks = append(blocks, anthropic.NewToolUseBlock(part.FunctionCall.ID, part.FunctionCall.Args, part.FunctionCall.Name))
		case part.FunctionResponse != nil:
			encoded, err := json.Marshal(part.FunctionResponse.Response)
			if err != nil {
				return nil, fmt.Errorf("encode function response %q: %w", part.FunctionResponse.Name, err)
			}
			_, isError := part.FunctionResponse.Response["error"]
			blocks = append(blocks, anthropic.NewToolResultBlock(part.FunctionResponse.ID, string(encoded), isError))
		case part.InlineData != nil:
			attachmentBlocks, err := toAnthropicAttachment(part.InlineData)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, attachmentBlocks...)
		case part.Text != "":
			blocks = append(blocks, anthropic.NewTextBlock(part.Text))
		}
	}
	return blocks, nil
}

func toAnthropicAttachment(
	blob *genai.Blob,
) ([]anthropic.ContentBlockParamUnion, error) {
	if blob == nil {
		return nil, nil
	}
	name := strings.TrimSpace(blob.DisplayName)
	var block anthropic.ContentBlockParamUnion
	switch blob.MIMEType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		block = anthropic.NewImageBlockBase64(
			blob.MIMEType,
			base64.StdEncoding.EncodeToString(blob.Data),
		)
	case "application/pdf":
		block = anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
			Data: base64.StdEncoding.EncodeToString(blob.Data),
		})
	case "application/json",
		"application/javascript",
		"application/sql",
		"application/toml",
		"application/xml",
		"application/yaml",
		"application/x-javascript",
		"application/x-sh",
		"application/x-yaml":
		block = anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
			Data: string(blob.Data),
		})
	default:
		if !strings.HasPrefix(blob.MIMEType, "text/") {
			return nil, fmt.Errorf(
				"anthropic attachment %q has unsupported MIME type %q",
				name,
				blob.MIMEType,
			)
		}
		block = anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
			Data: string(blob.Data),
		})
	}
	if block.OfDocument != nil && name != "" {
		block.OfDocument.Title = param.NewOpt(name)
		return []anthropic.ContentBlockParamUnion{block}, nil
	}
	if name == "" {
		return []anthropic.ContentBlockParamUnion{block}, nil
	}
	return []anthropic.ContentBlockParamUnion{
		anthropic.NewTextBlock("Attached file: " + name),
		block,
	}, nil
}

func toAnthropicTools(tools []*genai.Tool) ([]anthropic.ToolUnionParam, error) {
	var result []anthropic.ToolUnionParam
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		for _, declaration := range tool.FunctionDeclarations {
			if declaration == nil {
				continue
			}
			schema, err := toolInputSchema(declaration)
			if err != nil {
				return nil, fmt.Errorf("convert schema for tool %q: %w", declaration.Name, err)
			}
			parameter := anthropic.ToolParam{
				Name:        declaration.Name,
				Description: anthropic.String(declaration.Description),
				InputSchema: schema,
				Type:        anthropic.ToolTypeCustom,
			}
			result = append(result, anthropic.ToolUnionParam{OfTool: &parameter})
		}
	}
	return result, nil
}

func toolInputSchema(declaration *genai.FunctionDeclaration) (anthropic.ToolInputSchemaParam, error) {
	raw, err := toolJSONSchema(declaration)
	if err != nil {
		return anthropic.ToolInputSchemaParam{}, err
	}
	result := anthropic.ToolInputSchemaParam{Properties: map[string]any{}, ExtraFields: map[string]any{}}
	if properties, ok := raw["properties"]; ok {
		result.Properties = properties
	}
	if required, ok := raw["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				result.Required = append(result.Required, name)
			}
		}
	}
	for key, item := range raw {
		if key != "type" && key != "properties" && key != "required" {
			result.ExtraFields[key] = item
		}
	}
	return result, nil
}

func fromAnthropicMessage(message *anthropic.Message, partial, turnComplete bool) *model.LLMResponse {
	parts := make([]*genai.Part, 0, len(message.Content))
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, genai.NewPartFromText(block.Text))
			}
		case "tool_use":
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
				ID:   block.ID,
				Name: block.Name,
				Args: decodeFunctionArguments(string(block.Input)),
			}})
		}
	}
	return &model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: parts},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(message.Usage.InputTokens),
			CandidatesTokenCount: int32(message.Usage.OutputTokens),
			TotalTokenCount:      int32(message.Usage.InputTokens + message.Usage.OutputTokens),
		},
		ModelVersion: string(message.Model),
		Partial:      partial,
		TurnComplete: turnComplete,
		FinishReason: finishReason(message.StopReason),
	}
}

func finishReason(reason anthropic.StopReason) genai.FinishReason {
	switch reason {
	case "max_tokens":
		return genai.FinishReasonMaxTokens
	case "refusal":
		return genai.FinishReasonSafety
	case "", "end_turn", "stop_sequence", "tool_use", "pause_turn":
		return genai.FinishReasonStop
	default:
		return genai.FinishReasonOther
	}
}
