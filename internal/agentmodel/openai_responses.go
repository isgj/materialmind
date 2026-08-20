package agentmodel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	openairesponses "github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const openAIResponsesContextPrefix = "materialmind.openai-responses.v1:"

var _ model.LLM = (*OpenAIResponses)(nil)

// OpenAIResponses adapts OpenAI-compatible Responses APIs to ADK.
type OpenAIResponses struct {
	responses          openairesponses.ResponseService
	model              string
	contextScopePrefix string
	generationSettings GenerationSettings
}

type openAIResponsesContext struct {
	Scope  string            `json:"scope"`
	Output []json.RawMessage `json:"output"`
}

func NewOpenAIResponses(
	modelName, baseURL, bearerTokenEnvVar string,
	generationSettings GenerationSettings,
) (*OpenAIResponses, error) {
	return newOpenAIResponses(
		modelName,
		baseURL,
		bearerTokenEnvVar,
		"",
		bearerTokenEnvVar,
		generationSettings,
	)
}

func newOpenAIResponses(
	modelName, baseURL, bearerTokenEnvVar, bearerToken, credentialScope string,
	generationSettings GenerationSettings,
) (*OpenAIResponses, error) {
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

	return &OpenAIResponses{
		responses:          openairesponses.NewResponseService(requestOptions...),
		model:              modelName,
		contextScopePrefix: openAIResponsesScopePrefix(baseURL, credentialScope),
		generationSettings: generationSettings,
	}, nil
}

func (p *openAIResponsesProvider) ListModels(ctx context.Context) ([]AvailableModel, error) {
	return listOpenAIModels(ctx, p.baseURL, p.bearerTokenEnvVar, p.bearerToken)
}

func (m *OpenAIResponses) Name() string { return m.model }

func (m *OpenAIResponses) GenerateContent(
	ctx context.Context,
	req *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		modelName := openAIResponsesModelName(req, m.model)
		contextScope := openAIResponsesContextScope(m.contextScopePrefix, modelName)
		params, err := toOpenAIResponsesRequest(req, modelName, m.generationSettings, contextScope)
		if err != nil {
			yield(nil, err)
			return
		}
		if !stream {
			response, err := m.responses.New(ctx, params)
			if err != nil {
				yield(nil, fmt.Errorf("openai responses request: %w", err))
				return
			}
			converted, err := fromOpenAIResponsesResponse(response, contextScope)
			yield(converted, err)
			return
		}

		responseStream := m.responses.NewStreaming(ctx, params)
		defer responseStream.Close()
		var finalResponse *openairesponses.Response
		var reasoningPart [2]int64
		reasoningPartEmitted := false
		for responseStream.Next() {
			event := responseStream.Current()
			switch event.Type {
			case "response.reasoning_summary_text.delta":
				deltaEvent := event.AsResponseReasoningSummaryTextDelta()
				if deltaEvent.Delta == "" {
					continue
				}
				// The completed response joins reasoning summary parts with
				// a blank line. Emit the same separator when a new part
				// starts so the streamed thought text keeps the paragraph
				// structure of the final one.
				summaryPart := [2]int64{deltaEvent.OutputIndex, deltaEvent.SummaryIndex}
				if reasoningPartEmitted && summaryPart != reasoningPart &&
					!yield(openAIResponsesPartial("\n\n", modelName, true), nil) {
					return
				}
				reasoningPart = summaryPart
				reasoningPartEmitted = true
				if !yield(openAIResponsesPartial(deltaEvent.Delta, modelName, true), nil) {
					return
				}
			case "response.output_text.delta":
				delta := event.AsResponseOutputTextDelta().Delta
				if delta != "" && !yield(openAIResponsesPartial(delta, modelName, false), nil) {
					return
				}
			case "response.refusal.delta":
				delta := event.AsResponseRefusalDelta().Delta
				if delta != "" && !yield(openAIResponsesPartial(delta, modelName, false), nil) {
					return
				}
			case "response.completed":
				response := event.AsResponseCompleted().Response
				finalResponse = &response
			case "response.incomplete":
				response := event.AsResponseIncomplete().Response
				finalResponse = &response
			case "response.failed":
				response := event.AsResponseFailed().Response
				yield(nil, openAIResponsesTerminalError(&response))
				return
			case "error":
				responseError := event.AsError()
				yield(nil, fmt.Errorf("openai responses stream: %s: %s", responseError.Code, responseError.Message))
				return
			}
		}
		if err := responseStream.Err(); err != nil {
			yield(nil, fmt.Errorf("openai responses stream: %w", err))
			return
		}
		if finalResponse == nil {
			yield(nil, fmt.Errorf("openai responses stream ended without a terminal response"))
			return
		}
		converted, err := fromOpenAIResponsesResponse(finalResponse, contextScope)
		yield(converted, err)
	}
}

func openAIResponsesModelName(req *model.LLMRequest, fallback string) string {
	if req != nil {
		if name := strings.TrimSpace(req.Model); name != "" {
			return name
		}
	}
	return fallback
}

func openAIResponsesScopePrefix(baseURL, credentialScope string) string {
	configuredEndpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	configuredCredential := strings.TrimSpace(credentialScope)
	digest := sha256.Sum256([]byte(configuredEndpoint + "\x00" + configuredCredential))
	return hex.EncodeToString(digest[:])
}

func openAIResponsesContextScope(prefix, modelName string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + strings.TrimSpace(modelName)))
	return hex.EncodeToString(digest[:])
}

func openAIResponsesPartial(text, modelName string, thought bool) *model.LLMResponse {
	part := genai.NewPartFromText(text)
	part.Thought = thought
	return &model.LLMResponse{
		Content:      genai.NewContentFromParts([]*genai.Part{part}, genai.RoleModel),
		ModelVersion: modelName,
		Partial:      true,
	}
}

func toOpenAIResponsesRequest(
	req *model.LLMRequest,
	modelName string,
	generationSettings GenerationSettings,
	contextScope string,
) (openairesponses.ResponseNewParams, error) {
	if req == nil {
		return openairesponses.ResponseNewParams{}, fmt.Errorf("LLM request is required")
	}
	if generationSettings.MaxOutputTokens <= 0 {
		generationSettings.MaxOutputTokens = defaultMaxOutputTokens
	}
	params := openairesponses.ResponseNewParams{
		Model:           shared.ResponsesModel(modelName),
		MaxOutputTokens: param.NewOpt(generationSettings.MaxOutputTokens),
		Store:           param.NewOpt(false),
		Include: []openairesponses.ResponseIncludable{
			openairesponses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	reasoningEffort, err := openAIReasoningEffort(generationSettings.ReasoningEffort)
	if err != nil {
		return openairesponses.ResponseNewParams{}, err
	}
	params.Reasoning.Effort = reasoningEffort
	if reasoningEffort != "" {
		params.Reasoning.Summary = shared.ReasoningSummaryAuto
	}

	if req.Config != nil {
		if req.Config.MaxOutputTokens > 0 {
			params.MaxOutputTokens = param.NewOpt(int64(req.Config.MaxOutputTokens))
		}
		if systemInstruction := contentText(req.Config.SystemInstruction); systemInstruction != "" {
			params.Instructions = param.NewOpt(systemInstruction)
		}
		tools, err := toOpenAIResponsesTools(req.Config.Tools)
		if err != nil {
			return openairesponses.ResponseNewParams{}, err
		}
		params.Tools = tools
	}

	for _, content := range req.Contents {
		items, preserved, err := openAIResponsesPreservedItems(content, contextScope)
		if err != nil {
			return openairesponses.ResponseNewParams{}, err
		}
		if !preserved {
			items, err = toOpenAIResponsesItems(content)
			if err != nil {
				return openairesponses.ResponseNewParams{}, err
			}
		}
		params.Input.OfInputItemList = append(params.Input.OfInputItemList, items...)
	}
	if len(params.Input.OfInputItemList) == 0 {
		return openairesponses.ResponseNewParams{}, fmt.Errorf("at least one input item is required")
	}
	return params, nil
}

func toOpenAIResponsesItems(content *genai.Content) ([]openairesponses.ResponseInputItemUnionParam, error) {
	if content == nil {
		return nil, nil
	}
	role := openairesponses.EasyInputMessageRoleUser
	if content.Role == genai.RoleModel {
		role = openairesponses.EasyInputMessageRoleAssistant
	}

	var result []openairesponses.ResponseInputItemUnionParam
	var textParts []string
	var contentParts openairesponses.ResponseInputMessageContentListParam
	usesContentParts := false
	appendText := func(text string) {
		if usesContentParts {
			contentParts = append(
				contentParts,
				openairesponses.ResponseInputContentParamOfInputText(text),
			)
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
			contentParts = append(
				contentParts,
				openairesponses.ResponseInputContentParamOfInputText(text),
			)
		}
		textParts = nil
	}
	flushMessage := func() {
		if usesContentParts {
			if len(contentParts) > 0 {
				result = append(
					result,
					openairesponses.ResponseInputItemParamOfMessage(contentParts, role),
				)
			}
		} else if len(textParts) > 0 {
			result = append(
				result,
				openairesponses.ResponseInputItemParamOfMessage(strings.Join(textParts, ""), role),
			)
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
		case part.FunctionCall != nil:
			flushMessage()
			argumentsValue := part.FunctionCall.Args
			if argumentsValue == nil {
				argumentsValue = map[string]any{}
			}
			arguments, err := json.Marshal(argumentsValue)
			if err != nil {
				return nil, fmt.Errorf("encode function call %q: %w", part.FunctionCall.Name, err)
			}
			result = append(result, openairesponses.ResponseInputItemParamOfFunctionCall(
				string(arguments), part.FunctionCall.ID, part.FunctionCall.Name,
			))
		case part.FunctionResponse != nil:
			flushMessage()
			output, err := json.Marshal(part.FunctionResponse.Response)
			if err != nil {
				return nil, fmt.Errorf("encode function response %q: %w", part.FunctionResponse.Name, err)
			}
			result = append(result, openairesponses.ResponseInputItemParamOfFunctionCallOutput(
				part.FunctionResponse.ID, string(output),
			))
		case part.InlineData != nil:
			useContentParts()
			contentParts = append(contentParts, openAIResponsesAttachmentPart(part.InlineData))
		case part.Text != "":
			appendText(part.Text)
		}
	}
	flushMessage()
	return result, nil
}

func openAIResponsesAttachmentPart(
	blob *genai.Blob,
) openairesponses.ResponseInputContentUnionParam {
	encoded := base64.StdEncoding.EncodeToString(blob.Data)
	if strings.HasPrefix(blob.MIMEType, "image/") {
		part := openairesponses.ResponseInputContentParamOfInputImage(
			openairesponses.ResponseInputImageDetailAuto,
		)
		part.OfInputImage.ImageURL = param.NewOpt(
			"data:" + blob.MIMEType + ";base64," + encoded,
		)
		return part
	}
	filename := strings.TrimSpace(blob.DisplayName)
	if filename == "" {
		filename = "attachment"
	}
	return openairesponses.ResponseInputContentUnionParam{
		OfInputFile: &openairesponses.ResponseInputFileParam{
			FileData: param.NewOpt(encoded),
			Filename: param.NewOpt(filename),
			Detail:   openairesponses.ResponseInputFileDetailAuto,
		},
	}
}

func toOpenAIResponsesTools(tools []*genai.Tool) ([]openairesponses.ToolUnionParam, error) {
	var result []openairesponses.ToolUnionParam
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
			function := openairesponses.ToolParamOfFunction(declaration.Name, schema, false)
			if declaration.Description != "" {
				function.OfFunction.Description = param.NewOpt(declaration.Description)
			}
			result = append(result, function)
		}
	}
	return result, nil
}

func fromOpenAIResponsesResponse(
	response *openairesponses.Response,
	contextScope string,
) (*model.LLMResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("openai responses response is empty")
	}
	if err := openAIResponsesTerminalError(response); err != nil {
		return nil, err
	}

	parts := make([]*genai.Part, 0, len(response.Output)+1)
	if len(response.Output) > 0 {
		signature, err := encodeOpenAIResponsesContext(response.Output, contextScope)
		if err != nil {
			return nil, err
		}
		parts = append(parts, &genai.Part{Thought: true, ThoughtSignature: signature})
	}
	refused := false
	for _, item := range response.Output {
		switch item.Type {
		case "reasoning":
			reasoning := item.AsReasoning()
			summary := make([]string, 0, len(reasoning.Summary))
			for _, part := range reasoning.Summary {
				if text := strings.TrimSpace(part.Text); text != "" {
					summary = append(summary, text)
				}
			}
			if len(summary) > 0 {
				parts = append(parts, &genai.Part{
					Text:    strings.Join(summary, "\n\n"),
					Thought: true,
				})
			}
		case "message":
			message := item.AsMessage()
			for _, content := range message.Content {
				switch content.Type {
				case "output_text":
					if content.Text != "" {
						parts = append(parts, genai.NewPartFromText(content.Text))
					}
				case "refusal":
					refused = true
					if content.Refusal != "" {
						parts = append(parts, genai.NewPartFromText(content.Refusal))
					}
				}
			}
		case "function_call":
			call := item.AsFunctionCall()
			callID := call.CallID
			if callID == "" {
				callID = call.ID
			}
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
				ID:   callID,
				Name: call.Name,
				Args: decodeFunctionArguments(call.Arguments),
			}})
		}
	}

	reasoningTokens := response.Usage.OutputTokensDetails.ReasoningTokens
	visibleOutputTokens := response.Usage.OutputTokens - reasoningTokens
	if visibleOutputTokens < 0 {
		visibleOutputTokens = 0
	}
	finishReason := openAIResponsesFinishReason(response)
	if refused {
		finishReason = genai.FinishReasonSafety
	}
	return &model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: parts},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        int32(response.Usage.InputTokens),
			CachedContentTokenCount: int32(response.Usage.InputTokensDetails.CachedTokens),
			CandidatesTokenCount:    int32(visibleOutputTokens),
			ThoughtsTokenCount:      int32(reasoningTokens),
			TotalTokenCount:         int32(response.Usage.TotalTokens),
		},
		ModelVersion: string(response.Model),
		TurnComplete: true,
		FinishReason: finishReason,
	}, nil
}

func openAIResponsesTerminalError(response *openairesponses.Response) error {
	if response == nil {
		return fmt.Errorf("openai responses response is empty")
	}
	switch response.Status {
	case "", openairesponses.ResponseStatusCompleted, openairesponses.ResponseStatusIncomplete:
		return nil
	case openairesponses.ResponseStatusFailed:
		if response.Error.Message != "" {
			return fmt.Errorf("openai responses failed: %s: %s", response.Error.Code, response.Error.Message)
		}
		return fmt.Errorf("openai responses failed")
	case openairesponses.ResponseStatusCancelled:
		return fmt.Errorf("openai responses request was cancelled")
	default:
		return fmt.Errorf("openai responses returned non-terminal status %q", response.Status)
	}
}

func openAIResponsesFinishReason(response *openairesponses.Response) genai.FinishReason {
	if response.Status != openairesponses.ResponseStatusIncomplete {
		return genai.FinishReasonStop
	}
	switch response.IncompleteDetails.Reason {
	case "max_output_tokens":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	default:
		return genai.FinishReasonOther
	}
}

func encodeOpenAIResponsesContext(
	output []openairesponses.ResponseOutputItemUnion,
	contextScope string,
) ([]byte, error) {
	context := openAIResponsesContext{Scope: contextScope, Output: make([]json.RawMessage, 0, len(output))}
	for _, item := range output {
		raw := item.RawJSON()
		if raw == "" {
			return nil, fmt.Errorf("preserve openai responses output item %q: raw JSON is unavailable", item.Type)
		}
		context.Output = append(context.Output, json.RawMessage(raw))
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		return nil, fmt.Errorf("encode openai responses context: %w", err)
	}
	return append([]byte(openAIResponsesContextPrefix), encoded...), nil
}

func openAIResponsesPreservedItems(
	content *genai.Content,
	contextScope string,
) ([]openairesponses.ResponseInputItemUnionParam, bool, error) {
	if content == nil {
		return nil, false, nil
	}
	for _, part := range content.Parts {
		if part == nil || len(part.ThoughtSignature) == 0 {
			continue
		}
		signature := string(part.ThoughtSignature)
		if !strings.HasPrefix(signature, openAIResponsesContextPrefix) {
			continue
		}
		var preserved openAIResponsesContext
		if err := json.Unmarshal([]byte(strings.TrimPrefix(signature, openAIResponsesContextPrefix)), &preserved); err != nil {
			return nil, false, fmt.Errorf("decode preserved openai responses context: %w", err)
		}
		if preserved.Scope != contextScope {
			continue
		}
		items := make([]openairesponses.ResponseInputItemUnionParam, 0, len(preserved.Output))
		for _, raw := range preserved.Output {
			if len(raw) == 0 {
				return nil, false, fmt.Errorf("decode preserved openai responses context: empty output item")
			}
			items = append(items, param.Override[openairesponses.ResponseInputItemUnionParam](raw))
		}
		return items, true, nil
	}
	return nil, false, nil
}

// FilterPreservedResponseFunctionCalls removes selected function calls from
// adapter-owned response context after an agent response is split into
// multiple protocol-valid turns. Context produced by other adapters is left
// untouched.
func FilterPreservedResponseFunctionCalls(
	content *genai.Content,
	excludedIDs map[string]struct{},
) error {
	if content == nil || len(excludedIDs) == 0 {
		return nil
	}
	for _, part := range content.Parts {
		if part == nil || len(part.ThoughtSignature) == 0 {
			continue
		}
		signature := string(part.ThoughtSignature)
		if !strings.HasPrefix(signature, openAIResponsesContextPrefix) {
			continue
		}
		var preserved openAIResponsesContext
		if err := json.Unmarshal(
			[]byte(strings.TrimPrefix(signature, openAIResponsesContextPrefix)),
			&preserved,
		); err != nil {
			return fmt.Errorf("decode preserved openai responses context: %w", err)
		}
		filtered := make([]json.RawMessage, 0, len(preserved.Output))
		changed := false
		for _, raw := range preserved.Output {
			var identity struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
			}
			if err := json.Unmarshal(raw, &identity); err != nil {
				return fmt.Errorf("inspect preserved openai responses output item: %w", err)
			}
			callID := identity.CallID
			if callID == "" {
				callID = identity.ID
			}
			if identity.Type == "function_call" {
				if _, excluded := excludedIDs[callID]; excluded {
					changed = true
					continue
				}
			}
			filtered = append(filtered, raw)
		}
		if !changed {
			continue
		}
		preserved.Output = filtered
		encoded, err := json.Marshal(preserved)
		if err != nil {
			return fmt.Errorf("encode filtered openai responses context: %w", err)
		}
		part.ThoughtSignature = append(
			[]byte(openAIResponsesContextPrefix),
			encoded...,
		)
	}
	return nil
}
