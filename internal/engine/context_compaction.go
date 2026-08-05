package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"mime"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

const (
	contextCompactionStateKey    = "_materialmind_context_compaction_v1"
	contextCompactionMetadataKey = "_materialmind_context_compaction_event_v1"
	contextCompactionEventType   = "context_compaction"

	contextCompactionTriggerPercent = int64(90)
	contextCompactionTargetPercent  = int64(30)
	contextCompactionSummaryPercent = int64(1)

	contextCompactionCharsPerToken = int64(4)
	contextCompactionMinSummary    = int64(512)
	contextCompactionMaxSummary    = int64(8192)
)

const contextCompactionInstruction = `Compact the supplied coding-agent history into a precise continuation summary.

Preserve:
- the user's current objective and explicit constraints;
- decisions already made and their rationale;
- workspace paths, relevant files, symbols, commands, and concrete results;
- edits already applied and validation already run;
- active plans, unresolved errors, pending work, approvals, and questions.

Discard redundant logs, repeated output, conversational filler, and private chain-of-thought. Do not invent facts. Write the summary as durable context for another agent that must continue the work without the omitted transcript.`

type contextCompactor struct {
	summarizer          model.LLM
	maxContextTokens    int64
	maxOutputTokens     int64
	summaryOutputTokens int64
	onUpdate            func(agent.Context, contextCompactionUpdate)
}

type contextCompactionUpdate struct {
	ID                    string `json:"id"`
	Status                string `json:"status"`
	EstimatedTokensBefore int64  `json:"estimatedTokensBefore"`
	EstimatedTokensAfter  int64  `json:"estimatedTokensAfter,omitempty"`
	MaxContextTokens      int64  `json:"maxContextTokens"`
	SummarizedContents    int    `json:"summarizedContents"`
	Error                 string `json:"error,omitempty"`
}

type contextCompactionCheckpoint struct {
	Version            int    `json:"version"`
	PrefixContentCount int    `json:"prefixContentCount"`
	PrefixDigest       string `json:"prefixDigest"`
	Summary            string `json:"summary"`
}

func newContextCompactor(
	summarizer model.LLM,
	maxContextTokens, maxOutputTokens int64,
) *contextCompactor {
	return &contextCompactor{
		summarizer:          summarizer,
		maxContextTokens:    maxContextTokens,
		maxOutputTokens:     maxOutputTokens,
		summaryOutputTokens: contextCompactionSummaryTokenLimit(maxContextTokens),
	}
}

func (c *contextCompactor) beforeModel(
	ctx agent.Context,
	request *model.LLMRequest,
) (_ *model.LLMResponse, returnErr error) {
	if c == nil || c.summarizer == nil || c.maxContextTokens <= 0 || request == nil {
		return nil, nil
	}

	originalContents := request.Contents
	fullTokens, err := c.estimateRequestTokens(request, originalContents)
	if err != nil {
		return nil, fmt.Errorf("estimate model context: %w", err)
	}
	triggerTokens := percentage(c.maxContextTokens, contextCompactionTriggerPercent)
	if fullTokens < triggerTokens {
		return nil, nil
	}

	workingContents := originalContents
	prefixContentCount := 0
	insertedSummaryContents := 0
	checkpoint, found, err := loadContextCompactionCheckpoint(ctx, originalContents)
	if err != nil {
		return nil, fmt.Errorf("load context compaction checkpoint: %w", err)
	}
	if found {
		workingContents = contentsWithContextSummary(
			checkpoint.Summary,
			originalContents[checkpoint.PrefixContentCount:],
		)
		prefixContentCount = checkpoint.PrefixContentCount
		// The summary may be merged into the first retained user content instead
		// of occupying its own content, so only subtract what was actually inserted.
		insertedSummaryContents = len(workingContents) -
			(len(originalContents) - prefixContentCount)
		if insertedSummaryContents < 0 || insertedSummaryContents > 1 {
			return nil, fmt.Errorf(
				"compact model context: invalid summary content count %d",
				insertedSummaryContents,
			)
		}
		compactedTokens, estimateErr := c.estimateRequestTokens(request, workingContents)
		if estimateErr != nil {
			return nil, fmt.Errorf("estimate compacted model context: %w", estimateErr)
		}
		if compactedTokens < triggerTokens {
			request.Contents = workingContents
			return nil, nil
		}
	}

	cut, err := c.compactionCut(request, workingContents, found)
	if err != nil {
		return nil, err
	}
	if cut <= 0 {
		request.Contents = workingContents
		return nil, nil
	}

	newPrefixContentCount := cut
	if found {
		newPrefixContentCount = prefixContentCount + cut - insertedSummaryContents
	}
	if newPrefixContentCount <= prefixContentCount ||
		newPrefixContentCount > len(originalContents) {
		return nil, fmt.Errorf(
			"compact model context: invalid checkpoint advance from %d to %d",
			prefixContentCount,
			newPrefixContentCount,
		)
	}
	update := contextCompactionUpdate{
		ID: fmt.Sprintf(
			"context-compaction:%s:%d",
			ctx.InvocationID(),
			newPrefixContentCount,
		),
		Status:                "running",
		EstimatedTokensBefore: fullTokens,
		MaxContextTokens:      c.maxContextTokens,
		SummarizedContents:    newPrefixContentCount,
	}
	c.notify(ctx, update)
	terminalNotified := false
	defer func() {
		if returnErr == nil || terminalNotified {
			return
		}
		update.Status = "failed"
		if errors.Is(returnErr, context.Canceled) {
			update.Status = "cancelled"
		}
		update.Error = returnErr.Error()
		c.notify(ctx, update)
	}()

	summary, err := c.summarize(ctx, request.Model, workingContents[:cut])
	if err != nil {
		return nil, fmt.Errorf("compact model context: %w", err)
	}

	digest, err := contentDigest(originalContents[:newPrefixContentCount])
	if err != nil {
		return nil, fmt.Errorf("digest compacted model context: %w", err)
	}
	checkpoint = contextCompactionCheckpoint{
		Version:            1,
		PrefixContentCount: newPrefixContentCount,
		PrefixDigest:       digest,
		Summary:            summary,
	}
	if err := saveContextCompactionCheckpoint(ctx, checkpoint); err != nil {
		return nil, fmt.Errorf("save context compaction checkpoint: %w", err)
	}

	request.Contents = contentsWithContextSummary(
		summary,
		originalContents[newPrefixContentCount:],
	)
	compactedTokens, err := c.estimateRequestTokens(request, request.Contents)
	if err != nil {
		return nil, fmt.Errorf("estimate compacted model context: %w", err)
	}
	slog.Info(
		"compacted model context",
		"session_id", ctx.SessionID(),
		"estimated_tokens_before", fullTokens,
		"estimated_tokens_after", compactedTokens,
		"max_context_tokens", c.maxContextTokens,
		"summarized_contents", newPrefixContentCount,
	)
	update.Status = "completed"
	update.EstimatedTokensAfter = compactedTokens
	c.notify(ctx, update)
	terminalNotified = true
	return nil, nil
}

func (c *contextCompactor) notify(ctx agent.Context, update contextCompactionUpdate) {
	if c != nil && c.onUpdate != nil {
		c.onUpdate(ctx, update)
	}
}

func (e *Engine) handleContextCompaction(
	ctx agent.Context,
	runID string,
	update contextCompactionUpdate,
) {
	e.hub.Publish(runID, contextCompactionEventType, update)
	if update.Status == "running" {
		return
	}
	event := session.NewEvent(ctx, ctx.InvocationID())
	event.ID = update.ID
	event.Author = "workspace_agent"
	event.CustomMetadata = map[string]any{contextCompactionMetadataKey: update}
	if err := e.sessionService.AppendTranscriptEvent(
		ctx,
		AppName,
		UserID,
		ctx.SessionID(),
		event,
	); err != nil {
		slog.WarnContext(
			ctx,
			"persist context compaction event",
			"run_id",
			runID,
			"error",
			err,
		)
	}
}

func contextCompactionTranscriptItem(
	event *session.Event,
	runRecord store.Run,
) (store.TranscriptItem, bool) {
	if event == nil || event.CustomMetadata == nil {
		return store.TranscriptItem{}, false
	}
	raw, ok := event.CustomMetadata[contextCompactionMetadataKey]
	if !ok {
		return store.TranscriptItem{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return store.TranscriptItem{}, false
	}
	var update contextCompactionUpdate
	if err := json.Unmarshal(encoded, &update); err != nil || update.ID == "" {
		return store.TranscriptItem{}, false
	}
	input := map[string]any{
		"title":                 "Compact context",
		"estimatedTokensBefore": update.EstimatedTokensBefore,
		"maxContextTokens":      update.MaxContextTokens,
		"summarizedContents":    update.SummarizedContents,
	}
	output := map[string]any{"state": update.Status}
	if update.EstimatedTokensAfter > 0 {
		output["estimatedTokensAfter"] = update.EstimatedTokensAfter
	}
	if update.Error != "" {
		output["error"] = update.Error
	}
	return store.TranscriptItem{
		ID:           event.ID,
		InvocationID: event.InvocationID,
		Kind:         "context_compaction",
		ToolName:     "context_compaction",
		ToolInput:    input,
		ToolOutput:   output,
		AgentName:    event.Author,
		Provider:     runRecord.APICompatibility,
		Model:        runRecord.ModelID,
		CreatedAt:    event.Timestamp,
	}, true
}

func (c *contextCompactor) compactionCut(
	request *model.LLMRequest,
	contents []*genai.Content,
	hasCheckpoint bool,
) (int, error) {
	fixedTokens, err := c.estimateFixedTokens(request)
	if err != nil {
		return 0, fmt.Errorf("estimate fixed model context: %w", err)
	}
	targetTokens := percentage(c.maxContextTokens, contextCompactionTargetPercent)
	suffixBudget := targetTokens - fixedTokens - c.outputTokenReserve(request) -
		c.summaryOutputTokens
	suffixBudget = max(suffixBudget, 0)

	cut := len(contents)
	var suffixTokens int64
	for index := len(contents) - 1; index >= 0; index-- {
		contentTokens, estimateErr := estimateJSONTokens(contents[index])
		if estimateErr != nil {
			return 0, fmt.Errorf("estimate model content %d: %w", index, estimateErr)
		}
		if suffixTokens+contentTokens > suffixBudget {
			cut = index + 1
			break
		}
		suffixTokens += contentTokens
		cut = index
	}

	minimumCut := 1
	if hasCheckpoint {
		minimumCut = 2
	}
	cut = max(cut, minimumCut)
	for cut < len(contents) && !safeContextCompactionBoundary(contents, cut) {
		cut++
	}
	if cut > len(contents) {
		cut = len(contents)
	}
	if hasCheckpoint && len(contents) < 2 {
		return 0, fmt.Errorf(
			"compact model context: tools, instructions, and output allowance exceed the context budget",
		)
	}
	return cut, nil
}

func (c *contextCompactor) estimateRequestTokens(
	request *model.LLMRequest,
	contents []*genai.Content,
) (int64, error) {
	fixedTokens, err := c.estimateFixedTokens(request)
	if err != nil {
		return 0, err
	}
	result := fixedTokens + c.outputTokenReserve(request)
	for _, content := range contents {
		tokens, estimateErr := estimateJSONTokens(content)
		if estimateErr != nil {
			return 0, estimateErr
		}
		result += tokens
	}
	return result, nil
}

func (c *contextCompactor) estimateFixedTokens(request *model.LLMRequest) (int64, error) {
	if request == nil {
		return 0, nil
	}
	return estimateJSONTokens(struct {
		Model  string                       `json:"model,omitempty"`
		Config *genai.GenerateContentConfig `json:"config,omitempty"`
	}{
		Model:  request.Model,
		Config: request.Config,
	})
}

func (c *contextCompactor) outputTokenReserve(request *model.LLMRequest) int64 {
	if request != nil && request.Config != nil && request.Config.MaxOutputTokens > 0 {
		return int64(request.Config.MaxOutputTokens)
	}
	return max(c.maxOutputTokens, 0)
}

func (c *contextCompactor) summarize(
	ctx context.Context,
	modelName string,
	contents []*genai.Content,
) (string, error) {
	sections, err := renderContextSections(contents)
	if err != nil {
		return "", err
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no context was available to summarize")
	}

	inputBudgetTokens := percentage(c.maxContextTokens, contextCompactionTargetPercent) -
		c.summaryOutputTokens
	inputBudgetTokens = max(inputBudgetTokens, 1024)
	inputBudgetChars := inputBudgetTokens * contextCompactionCharsPerToken
	sectionBudgetChars := inputBudgetChars -
		c.summaryOutputTokens*contextCompactionCharsPerToken - 1024
	sectionBudgetChars = max(sectionBudgetChars, 1024)

	var summary string
	var batch strings.Builder
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		next, summarizeErr := c.summarizeBatch(
			ctx,
			modelName,
			summary,
			batch.String(),
		)
		if summarizeErr != nil {
			return summarizeErr
		}
		summary = next
		batch.Reset()
		return nil
	}

	for _, section := range sections {
		for _, fragment := range splitContextSection(section, sectionBudgetChars) {
			available := inputBudgetChars - int64(len(summary)) -
				int64(batch.Len()) - 1024
			if batch.Len() > 0 && int64(len(fragment)) > available {
				if err := flush(); err != nil {
					return "", err
				}
			}
			if batch.Len() > 0 {
				batch.WriteString("\n\n")
			}
			batch.WriteString(fragment)
		}
	}
	if err := flush(); err != nil {
		return "", err
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summarizer returned an empty context")
	}
	return summary, nil
}

func (c *contextCompactor) summarizeBatch(
	ctx context.Context,
	modelName, previousSummary, transcript string,
) (string, error) {
	var prompt strings.Builder
	if previousSummary != "" {
		prompt.WriteString("Existing compacted context:\n")
		prompt.WriteString(previousSummary)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Additional history to incorporate:\n")
	prompt.WriteString(transcript)

	maxOutputTokens := min(c.summaryOutputTokens, int64(^uint32(0)>>1))
	request := &model.LLMRequest{
		Model: modelName,
		Contents: []*genai.Content{
			genai.NewContentFromText(prompt.String(), genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(
				contextCompactionInstruction,
				genai.RoleUser,
			),
			MaxOutputTokens: int32(maxOutputTokens),
		},
	}

	var result strings.Builder
	for response, err := range c.summarizer.GenerateContent(ctx, request, false) {
		if err != nil {
			return "", err
		}
		if response == nil || response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			if part != nil && part.Text != "" && !part.Thought {
				result.WriteString(part.Text)
			}
		}
	}
	return strings.TrimSpace(result.String()), nil
}

func loadContextCompactionCheckpoint(
	ctx agent.Context,
	contents []*genai.Content,
) (contextCompactionCheckpoint, bool, error) {
	raw, err := ctx.State().Get(contextCompactionStateKey)
	if errors.Is(err, session.ErrStateKeyNotExist) {
		return contextCompactionCheckpoint{}, false, nil
	}
	if err != nil {
		return contextCompactionCheckpoint{}, false, err
	}
	encoded, ok := raw.(string)
	if !ok {
		return contextCompactionCheckpoint{}, false, nil
	}
	var checkpoint contextCompactionCheckpoint
	if err := json.Unmarshal([]byte(encoded), &checkpoint); err != nil {
		return contextCompactionCheckpoint{}, false, nil
	}
	if checkpoint.Version != 1 ||
		checkpoint.PrefixContentCount <= 0 ||
		checkpoint.PrefixContentCount > len(contents) ||
		strings.TrimSpace(checkpoint.Summary) == "" {
		return contextCompactionCheckpoint{}, false, nil
	}
	digest, err := contentDigest(contents[:checkpoint.PrefixContentCount])
	if err != nil {
		return contextCompactionCheckpoint{}, false, err
	}
	if digest != checkpoint.PrefixDigest {
		return contextCompactionCheckpoint{}, false, nil
	}
	return checkpoint, true, nil
}

func saveContextCompactionCheckpoint(
	ctx agent.Context,
	checkpoint contextCompactionCheckpoint,
) error {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return ctx.State().Set(contextCompactionStateKey, string(encoded))
}

func contentsWithContextSummary(
	summary string,
	suffix []*genai.Content,
) []*genai.Content {
	summaryPart := genai.NewPartFromText(
		"Earlier conversation context was compacted into this summary:\n\n" +
			strings.TrimSpace(summary),
	)
	result := make([]*genai.Content, 0, len(suffix)+1)
	if len(suffix) > 0 &&
		suffix[0] != nil &&
		suffix[0].Role != genai.RoleModel &&
		!contentHasFunctionResponse(suffix[0]) {
		first := *suffix[0]
		first.Parts = append([]*genai.Part{summaryPart}, suffix[0].Parts...)
		result = append(result, &first)
		result = append(result, suffix[1:]...)
		return result
	}
	result = append(result, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{summaryPart},
	})
	result = append(result, suffix...)
	return result
}

func safeContextCompactionBoundary(contents []*genai.Content, cut int) bool {
	if cut <= 0 || cut >= len(contents) {
		return true
	}
	first := contents[cut]
	if first != nil && contentHasFunctionResponse(first) {
		return false
	}

	prefixCalls := make(map[string]struct{})
	for _, content := range contents[:cut] {
		for _, part := range contentPartsForCompaction(content) {
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				prefixCalls[part.FunctionCall.ID] = struct{}{}
			}
		}
	}
	if len(prefixCalls) == 0 {
		return true
	}
	for _, content := range contents[cut:] {
		for _, part := range contentPartsForCompaction(content) {
			if part.FunctionResponse == nil {
				continue
			}
			if _, crossesBoundary := prefixCalls[part.FunctionResponse.ID]; crossesBoundary {
				return false
			}
		}
	}
	return true
}

func contentHasFunctionResponse(content *genai.Content) bool {
	for _, part := range contentPartsForCompaction(content) {
		if part.FunctionResponse != nil {
			return true
		}
	}
	return false
}

func contentPartsForCompaction(content *genai.Content) []*genai.Part {
	if content == nil {
		return nil
	}
	return content.Parts
}

func renderContextSections(contents []*genai.Content) ([]string, error) {
	result := make([]string, 0, len(contents))
	for _, content := range contents {
		if content == nil {
			continue
		}
		var section strings.Builder
		role := strings.ToUpper(strings.TrimSpace(content.Role))
		if role == "" {
			role = "USER"
		}
		section.WriteString(role)
		section.WriteString(":\n")
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			rendered, err := renderContextPart(part)
			if err != nil {
				return nil, err
			}
			if rendered == "" {
				continue
			}
			section.WriteString(rendered)
			if !strings.HasSuffix(rendered, "\n") {
				section.WriteByte('\n')
			}
		}
		if strings.TrimSpace(section.String()) != role+":" {
			result = append(result, strings.TrimSpace(section.String()))
		}
	}
	return result, nil
}

func renderContextPart(part *genai.Part) (string, error) {
	switch {
	case part.Text != "":
		label := ""
		if part.Thought {
			label = "[agent reasoning]\n"
		}
		return label + part.Text, nil
	case part.FunctionCall != nil:
		args, err := json.Marshal(part.FunctionCall.Args)
		if err != nil {
			return "", fmt.Errorf("encode %q tool arguments: %w", part.FunctionCall.Name, err)
		}
		return fmt.Sprintf(
			"[tool call %s id=%s]\n%s",
			part.FunctionCall.Name,
			part.FunctionCall.ID,
			args,
		), nil
	case part.FunctionResponse != nil:
		response, err := json.Marshal(part.FunctionResponse.Response)
		if err != nil {
			return "", fmt.Errorf("encode %q tool result: %w", part.FunctionResponse.Name, err)
		}
		return fmt.Sprintf(
			"[tool result %s id=%s]\n%s",
			part.FunctionResponse.Name,
			part.FunctionResponse.ID,
			response,
		), nil
	case part.InlineData != nil:
		return renderInlineContextData(part.InlineData), nil
	case part.FileData != nil:
		return fmt.Sprintf(
			"[file %s mime=%s uri=%s]",
			part.FileData.DisplayName,
			part.FileData.MIMEType,
			part.FileData.FileURI,
		), nil
	case part.ExecutableCode != nil:
		return fmt.Sprintf(
			"[executable code language=%s]\n%s",
			part.ExecutableCode.Language,
			part.ExecutableCode.Code,
		), nil
	case part.CodeExecutionResult != nil:
		return fmt.Sprintf(
			"[code execution result outcome=%s]\n%s",
			part.CodeExecutionResult.Outcome,
			part.CodeExecutionResult.Output,
		), nil
	default:
		return "", nil
	}
}

func renderInlineContextData(data *genai.Blob) string {
	if data == nil {
		return ""
	}
	mediaType, _, _ := mime.ParseMediaType(data.MIMEType)
	if strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		mediaType == "application/javascript" ||
		mediaType == "application/xml" {
		return fmt.Sprintf(
			"[attached file %s mime=%s]\n%s",
			data.DisplayName,
			data.MIMEType,
			data.Data,
		)
	}
	return fmt.Sprintf(
		"[binary attachment %s mime=%s size=%d bytes]",
		data.DisplayName,
		data.MIMEType,
		len(data.Data),
	)
}

func splitContextSection(section string, maxChars int64) []string {
	if maxChars <= 0 || int64(len(section)) <= maxChars {
		return []string{section}
	}
	result := make([]string, 0, int64(len(section))/maxChars+1)
	for len(section) > 0 {
		end := min(int(maxChars), len(section))
		for end > 0 && end < len(section) && !utf8.RuneStart(section[end]) {
			end--
		}
		if end == 0 {
			end = min(int(maxChars), len(section))
		}
		if newline := strings.LastIndexByte(section[:end], '\n'); newline >= end/2 {
			end = newline + 1
		}
		result = append(result, section[:end])
		section = section[end:]
	}
	return result
}

func estimateJSONTokens(value any) (int64, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return max((int64(len(encoded))+contextCompactionCharsPerToken-1)/
		contextCompactionCharsPerToken, 1), nil
}

func contentDigest(contents []*genai.Content) (string, error) {
	digest := sha256.New()
	for _, content := range contents {
		if err := writeContentDigest(digest, content); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeContentDigest(digest hash.Hash, content *genai.Content) error {
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	if _, err := digest.Write(encoded); err != nil {
		return err
	}
	_, err = digest.Write([]byte{0})
	return err
}

func percentage(value, percent int64) int64 {
	return value/100*percent + value%100*percent/100
}

func contextCompactionSummaryTokenLimit(maxContextTokens int64) int64 {
	if maxContextTokens <= 0 {
		return 0
	}
	summaryTokens := maxContextTokens * contextCompactionSummaryPercent / 100
	summaryTokens = max(summaryTokens, contextCompactionMinSummary)
	summaryTokens = min(summaryTokens, contextCompactionMaxSummary)
	return min(summaryTokens, max(maxContextTokens/4, 1))
}
