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
	contextCompactionStateKey = "_materialmind_context_compaction_v1"
	// contextCompactionUsageKey stores the provider-measured prompt token
	// count of the last sent request so beforeModel can project usage for
	// providers that report usage.
	contextCompactionUsageKey    = "_materialmind_context_usage_v1"
	contextCompactionMetadataKey = "_materialmind_context_compaction_event_v1"
	contextCompactionEventType   = "context_compaction"

	contextCompactionTriggerPercent = int64(90)
	contextCompactionTargetPercent  = int64(30)
	contextCompactionSummaryPercent = int64(1)

	// contextCompactionBytesPerTwoTokens expresses the fallback token density
	// as 2.5 bytes/token (5 bytes per 2 tokens) using integer arithmetic.
	// Real agent payloads (JSON-heavy tool calls and results) measure about
	// 2.5 bytes/token; the previous 4 bytes/token under-estimated usage by
	// roughly 1.6x and let requests through that the provider rejected.
	contextCompactionBytesPerTwoTokens = int64(5)
	contextCompactionMinSummary        = int64(512)
	contextCompactionMaxSummary        = int64(8192)
)

// contextLengthErrorMarkers identifies provider rejections caused by an
// oversized prompt so the engine can force a compaction and unstick the
// session instead of failing every retry.
//
// Only unambiguous overflow phrases are matched: broad markers such as
// "max_tokens" or "token limit" also appear in parameter-validation and
// quota 400s (e.g. "Invalid value for 'max_tokens': must be between 1 and
// N"), and misclassifying those would bleed context lossily and mask the
// real error on every retry.
var contextLengthErrorMarkers = []string{
	"context length",
	"context_length",
	"context window",
	"too many tokens",
	"maximum context",
	"prompt is too long",
	"exceeds the context",
	"input + ",
}

// contextCompactionFallbackSummary is checkpointed when summarization fails,
// so the oversized prefix is still dropped from the request and the session
// can continue instead of re-failing on the same oversized request.
const contextCompactionFallbackSummary = "Earlier conversation context was dropped because it could not be summarized. Continue from the remaining conversation and re-inspect the workspace when earlier details are needed."

// contextCompactionDroppedTailNote is appended when the summarizer budget
// runs out before the whole prefix is folded in, so the omission stays
// visible in the continued conversation.
const contextCompactionDroppedTailNote = "Note: the most recent portion of the summarized history exceeded the compaction budget and was not included in this summary."

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

	// Per-run measured usage projection. beforeModel records the request it
	// sends (lastSentContentsCount/lastSentLastDigest); afterModel fills
	// measuredPromptTokens when the provider reports usage. The same record
	// is persisted to session state under contextCompactionUsageKey for the
	// next run.
	lastSentContentsCount int
	lastSentLastDigest    string
	measuredPromptTokens  int64
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
	// Budget the input against I_max = window - output reserve: the provider
	// rejects a call when input + output exceeds the window, so the trigger
	// is a percentage of I_max, not of the full window.
	triggerTokens := c.compactionTriggerTokens(request)
	inputTokens := fullTokens - c.outputTokenReserve(request)
	// Project against the contents that will actually be sent (checkpoint
	// re-applied): usage records are anchored to sent contents, so
	// validating them against the full, un-compacted contents would fail
	// after the first compaction and kill the measured path for the rest
	// of the session.
	projectedTokens := c.projectedInputTokens(
		ctx,
		c.checkpointedContents(ctx, originalContents),
	)
	decisionTokens := max(inputTokens, projectedTokens)
	if decisionTokens < triggerTokens {
		c.recordSentRequest(originalContents)
		return nil, nil
	}
	if projectedTokens > inputTokens {
		slog.InfoContext(
			ctx,
			"context compaction triggered by measured usage",
			"session_id", ctx.SessionID(),
			"measured_tokens", projectedTokens,
			"estimated_tokens", inputTokens,
			"trigger_tokens", triggerTokens,
		)
	}
	return nil, c.compactRequest(ctx, request, fullTokens, false)
}

// compactRequest summarizes and drops the oldest contents so the request
// fits the context budget. It is used both when the estimate or the
// measured-usage projection crosses the compaction trigger and when the
// provider has already rejected the request as too long (forced).
func (c *contextCompactor) compactRequest(
	ctx agent.Context,
	request *model.LLMRequest,
	estimatedTokens int64,
	forced bool,
) (returnErr error) {
	originalContents := request.Contents

	workingContents := originalContents
	prefixContentCount := 0
	insertedSummaryContents := 0
	checkpoint, found, err := loadContextCompactionCheckpoint(ctx, originalContents)
	if err != nil {
		return fmt.Errorf("load context compaction checkpoint: %w", err)
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
			return fmt.Errorf(
				"compact model context: invalid summary content count %d",
				insertedSummaryContents,
			)
		}
		compactedTokens, estimateErr := c.estimateInputTokens(request, workingContents)
		if estimateErr != nil {
			return fmt.Errorf("estimate compacted model context: %w", estimateErr)
		}
		if compactedTokens < c.compactionTriggerTokens(request) {
			request.Contents = workingContents
			c.recordSentRequest(workingContents)
			return nil
		}
	}

	cut, err := c.compactionCut(request, workingContents, found)
	if err != nil {
		return err
	}
	if cut <= 0 {
		request.Contents = workingContents
		c.recordSentRequest(workingContents)
		return nil
	}

	newPrefixContentCount := cut
	if found {
		newPrefixContentCount = prefixContentCount + cut - insertedSummaryContents
	}
	if newPrefixContentCount <= prefixContentCount ||
		newPrefixContentCount > len(originalContents) {
		return fmt.Errorf(
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
		EstimatedTokensBefore: estimatedTokens,
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

	// A summarizer failure must not fail the run on either path: drop the
	// prefix with a placeholder summary so the session continues below the
	// window instead of re-failing on the same oversized request (forced:
	// after a provider rejection; otherwise: estimate/usage trigger).
	summary, summarizeErr := c.summarize(ctx, request.Model, workingContents[:cut])
	if summarizeErr != nil {
		slog.WarnContext(
			ctx,
			"summarize model context failed; dropping prefix without summary",
			"session_id", ctx.SessionID(),
			"forced", forced,
			"error", summarizeErr.Error(),
		)
		summary = contextCompactionFallbackSummary
		update.Error = fmt.Sprintf("summarize: %v", summarizeErr)
	}

	digest, err := contentDigest(originalContents[:newPrefixContentCount])
	if err != nil {
		return fmt.Errorf("digest compacted model context: %w", err)
	}
	checkpoint = contextCompactionCheckpoint{
		Version:            1,
		PrefixContentCount: newPrefixContentCount,
		PrefixDigest:       digest,
		Summary:            summary,
	}
	if err := saveContextCompactionCheckpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("save context compaction checkpoint: %w", err)
	}

	request.Contents = contentsWithContextSummary(
		summary,
		originalContents[newPrefixContentCount:],
	)
	c.recordSentRequest(request.Contents)
	compactedTokens, err := c.estimateRequestTokens(request, request.Contents)
	if err != nil {
		return fmt.Errorf("estimate compacted model context: %w", err)
	}
	slog.Info(
		"compacted model context",
		"session_id", ctx.SessionID(),
		"estimated_tokens_before", estimatedTokens,
		"estimated_tokens_after", compactedTokens,
		"max_context_tokens", c.maxContextTokens,
		"summarized_contents", newPrefixContentCount,
	)
	update.Status = "completed"
	update.EstimatedTokensAfter = compactedTokens
	c.notify(ctx, update)
	terminalNotified = true
	return nil
}

// onModelError force-compacts the session context when the provider rejects
// the request as too long, so the next run starts from the checkpoint instead
// of re-failing on the same oversized request. The returned error replaces
// the raw provider rejection with an actionable message.
func (c *contextCompactor) onModelError(
	ctx agent.Context,
	request *model.LLMRequest,
	responseErr error,
) (*model.LLMResponse, error) {
	if c == nil || request == nil || responseErr == nil ||
		!isContextLengthError(responseErr) {
		return nil, responseErr
	}
	estimatedTokens := int64(0)
	if tokens, estimateErr := c.estimateRequestTokens(request, request.Contents); estimateErr == nil {
		estimatedTokens = tokens
	}
	if err := c.compactRequest(ctx, request, estimatedTokens, true); err != nil {
		slog.WarnContext(
			ctx,
			"forced context compaction failed",
			"session_id", ctx.SessionID(),
			"error", err.Error(),
		)
		return nil, responseErr
	}
	slog.WarnContext(
		ctx,
		"forced context compaction after model context-length error",
		"session_id", ctx.SessionID(),
		"model_error", responseErr.Error(),
	)
	return nil, errors.New(
		"the model context window was exceeded; the session context was compacted, send the next message to continue",
	)
}

func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range contextLengthErrorMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// afterModel records the provider-reported prompt token count of the request
// that produced the response, enabling measured-usage projection in later
// beforeModel calls. Providers that do not report usage are skipped.
func (c *contextCompactor) afterModel(
	ctx agent.Context,
	response *model.LLMResponse,
	responseErr error,
) (*model.LLMResponse, error) {
	if c == nil || response == nil || responseErr != nil ||
		c.lastSentContentsCount == 0 {
		return response, responseErr
	}
	if response.UsageMetadata == nil || response.UsageMetadata.PromptTokenCount <= 0 {
		return response, responseErr
	}
	c.measuredPromptTokens = int64(response.UsageMetadata.PromptTokenCount)
	record := contextUsageRecord{
		Version:           1,
		PromptTokens:      c.measuredPromptTokens,
		ContentsCount:     c.lastSentContentsCount,
		LastContentDigest: c.lastSentLastDigest,
	}
	if encoded, err := json.Marshal(record); err == nil {
		if setErr := ctx.State().Set(contextCompactionUsageKey, string(encoded)); setErr != nil {
			slog.WarnContext(
				ctx,
				"save context usage record",
				"session_id", ctx.SessionID(),
				"error", setErr.Error(),
			)
		}
	}
	return response, responseErr
}

type contextUsageRecord struct {
	Version           int    `json:"version"`
	PromptTokens      int64  `json:"promptTokens"`
	ContentsCount     int    `json:"contentsCount"`
	LastContentDigest string `json:"lastContentDigest"`
}

// recordSentRequest remembers the request that will be sent to the model so
// afterModel can pair the provider-reported usage with its contents.
func (c *contextCompactor) recordSentRequest(contents []*genai.Content) {
	if c == nil {
		return
	}
	c.lastSentContentsCount = 0
	c.lastSentLastDigest = ""
	c.measuredPromptTokens = 0
	if len(contents) == 0 {
		return
	}
	digest, err := contentDigest(contents[len(contents)-1:])
	if err != nil {
		return
	}
	c.lastSentContentsCount = len(contents)
	c.lastSentLastDigest = digest
}

// checkpointedContents returns the request contents as they will be sent
// after re-applying any existing compaction checkpoint, or the full contents
// when no checkpoint applies. Measured-usage projection must validate its
// record against this base because usage records are anchored to the
// contents that were actually sent.
func (c *contextCompactor) checkpointedContents(
	ctx agent.Context,
	contents []*genai.Content,
) []*genai.Content {
	checkpoint, found, err := loadContextCompactionCheckpoint(ctx, contents)
	if err != nil || !found {
		return contents
	}
	return contentsWithContextSummary(
		checkpoint.Summary,
		contents[checkpoint.PrefixContentCount:],
	)
}

// projectedInputTokens estimates the input size of the current request from
// the provider-measured usage of the last sent request plus the locally
// estimated size of the contents appended since then. It returns 0 when no
// valid measurement is available.
func (c *contextCompactor) projectedInputTokens(
	ctx agent.Context,
	contents []*genai.Content,
) int64 {
	record := contextUsageRecord{
		Version:           1,
		PromptTokens:      c.measuredPromptTokens,
		ContentsCount:     c.lastSentContentsCount,
		LastContentDigest: c.lastSentLastDigest,
	}
	if !validContextUsageRecord(record, contents) {
		stored, ok := loadContextUsageRecord(ctx)
		if !ok || !validContextUsageRecord(stored, contents) {
			return 0
		}
		record = stored
	}
	projected := record.PromptTokens
	for _, content := range contents[record.ContentsCount:] {
		tokens, err := estimateJSONTokens(content)
		if err != nil {
			return 0
		}
		projected += tokens
	}
	return projected
}

func validContextUsageRecord(record contextUsageRecord, contents []*genai.Content) bool {
	if record.Version != 1 || record.PromptTokens <= 0 {
		return false
	}
	if record.ContentsCount <= 0 || record.ContentsCount > len(contents) {
		return false
	}
	digest, err := contentDigest(contents[record.ContentsCount-1 : record.ContentsCount])
	if err != nil || digest != record.LastContentDigest {
		return false
	}
	return true
}

func loadContextUsageRecord(ctx agent.Context) (contextUsageRecord, bool) {
	raw, err := ctx.State().Get(contextCompactionUsageKey)
	if errors.Is(err, session.ErrStateKeyNotExist) {
		return contextUsageRecord{}, false
	}
	if err != nil {
		return contextUsageRecord{}, false
	}
	encoded, ok := raw.(string)
	if !ok {
		return contextUsageRecord{}, false
	}
	var record contextUsageRecord
	if err := json.Unmarshal([]byte(encoded), &record); err != nil {
		return contextUsageRecord{}, false
	}
	return record, true
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
	targetTokens := percentage(c.inputBudgetTokens(request), contextCompactionTargetPercent)
	suffixBudget := targetTokens - fixedTokens - c.summaryOutputTokens
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
	inputTokens, err := c.estimateInputTokens(request, contents)
	if err != nil {
		return 0, err
	}
	return inputTokens + c.outputTokenReserve(request), nil
}

func (c *contextCompactor) estimateInputTokens(
	request *model.LLMRequest,
	contents []*genai.Content,
) (int64, error) {
	fixedTokens, err := c.estimateFixedTokens(request)
	if err != nil {
		return 0, err
	}
	result := fixedTokens
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

// inputBudgetTokens is the input budget I_max = window - output reserve: the
// provider rejects calls whose input plus output allowance exceeds the
// window, so input estimates and cuts are budgeted against I_max.
func (c *contextCompactor) inputBudgetTokens(request *model.LLMRequest) int64 {
	budget := c.maxContextTokens - c.outputTokenReserve(request)
	if budget < 0 {
		budget = 0
	}
	return budget
}

func (c *contextCompactor) compactionTriggerTokens(request *model.LLMRequest) int64 {
	return percentage(c.inputBudgetTokens(request), contextCompactionTriggerPercent)
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

	// The summarizer runs on the same model with the same window: keep its
	// own call (instruction + prompt + summary output) inside
	// min(30% window, I_max) so it can never be rejected as too long.
	instructionTokens, err := estimateJSONTokens(contextCompactionInstruction)
	if err != nil {
		return "", fmt.Errorf("estimate summarizer instruction: %w", err)
	}
	inputBudgetTokens := min(
		percentage(c.maxContextTokens, contextCompactionTargetPercent),
		c.inputBudgetTokens(nil),
	)
	inputBudgetTokens -= c.summaryOutputTokens + instructionTokens
	inputBudgetTokens = max(inputBudgetTokens, 1024)
	inputBudgetChars := tokensToContextChars(inputBudgetTokens)
	sectionBudgetChars := inputBudgetChars -
		tokensToContextChars(c.summaryOutputTokens) - 1024
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
		// Never regress the accumulated summary: a batch that comes back
		// without visible text loses only its own content, and the loss
		// stays visible in the log instead of discarding earlier batches.
		if strings.TrimSpace(next) == "" {
			slog.WarnContext(
				ctx,
				"context summarizer returned no visible text for a batch; keeping previous summary",
			)
		} else {
			summary = next
		}
		batch.Reset()
		return nil
	}

	droppedTail := false
	// Strictly bound the summarizer prompt: flush the batch when a fragment
	// no longer fits, truncate oversized fragments, and stop once the budget
	// is exhausted so the summarizer call can never overflow the context.
outer:
	for _, section := range sections {
		for _, fragment := range splitContextSection(section, sectionBudgetChars) {
			available := inputBudgetChars - int64(len(summary)) -
				int64(batch.Len()) - 1024
			if batch.Len() > 0 && int64(len(fragment)) > available {
				if err := flush(); err != nil {
					return "", err
				}
				available = inputBudgetChars - int64(len(summary)) -
					int64(batch.Len()) - 1024
			}
			if available <= 0 {
				droppedTail = true
				break outer
			}
			if int64(len(fragment)) > available {
				fragment = truncateContextFragment(fragment, available)
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
	if droppedTail {
		summary = summary + "\n\n" + contextCompactionDroppedTailNote
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
	return max((2*int64(len(encoded))+contextCompactionBytesPerTwoTokens-1)/
		contextCompactionBytesPerTwoTokens, 1), nil
}

// tokensToContextChars converts a token budget to a character budget at the
// fallback density (2.5 chars/token).
func tokensToContextChars(tokens int64) int64 {
	return tokens * contextCompactionBytesPerTwoTokens / 2
}

// truncateContextFragment cuts a fragment to maxChars on a UTF-8 boundary so
// the summarizer prompt stays within its budget even for oversized tool
// results.
func truncateContextFragment(fragment string, maxChars int64) string {
	if maxChars <= 0 || int64(len(fragment)) <= maxChars {
		return fragment
	}
	end := int(maxChars)
	for end > 0 && !utf8.RuneStart(fragment[end]) {
		end--
	}
	return fragment[:end]
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
