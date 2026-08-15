package engine

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

func TestContextCompactorLeavesRequestBelowThresholdUntouched(t *testing.T) {
	t.Parallel()

	summarizer := &compactionTestModel{summary: "unused"}
	compactor := newContextCompactor(summarizer, 10_000, 1_000)
	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-below-threshold",
	}
	content := genai.NewContentFromText(strings.Repeat("a", 1_000), genai.RoleUser)
	request := &model.LLMRequest{
		Model:    "test-model",
		Contents: []*genai.Content{content},
	}

	response, err := compactor.beforeModel(ctx, request)
	if err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	if response != nil {
		t.Fatalf("beforeModel() response = %#v, want nil", response)
	}
	if summarizer.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0", summarizer.calls)
	}
	if len(request.Contents) != 1 || request.Contents[0] != content {
		t.Fatalf("request contents were changed below the compaction threshold")
	}
	if _, ok := state[contextCompactionStateKey]; ok {
		t.Fatalf("compaction checkpoint was saved below the threshold")
	}
}

func TestContextCompactorCompactsAndReusesCheckpoint(t *testing.T) {
	t.Parallel()

	summarizer := &compactionTestModel{summary: "Durable compacted context."}
	compactor := newContextCompactor(summarizer, 30_000, 6_000)
	var updates []contextCompactionUpdate
	compactor.onUpdate = func(_ agent.Context, update contextCompactionUpdate) {
		updates = append(updates, update)
	}
	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-with-checkpoint",
	}
	original := []*genai.Content{
		genai.NewContentFromText(strings.Repeat("a", 23_000), genai.RoleUser),
		genai.NewContentFromText(strings.Repeat("b", 23_000), genai.RoleModel),
		genai.NewContentFromText(strings.Repeat("c", 23_000), genai.RoleUser),
		genai.NewContentFromText(strings.Repeat("d", 23_000), genai.RoleModel),
	}
	request := &model.LLMRequest{Model: "test-model", Contents: original}

	if _, err := compactor.beforeModel(ctx, request); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	callsAfterCompaction := summarizer.calls
	if callsAfterCompaction == 0 {
		t.Fatal("summarizer was not called above the compaction threshold")
	}
	if len(updates) != 2 || updates[0].Status != "running" || updates[1].Status != "completed" {
		t.Fatalf("compaction updates = %#v", updates)
	}
	if updates[0].ID == "" ||
		updates[0].EstimatedTokensBefore <= updates[1].EstimatedTokensAfter ||
		updates[1].SummarizedContents <= 0 {
		t.Fatalf("compaction update metrics = %#v", updates)
	}
	rawCheckpoint, ok := state[contextCompactionStateKey].(string)
	if !ok || rawCheckpoint == "" {
		t.Fatal("compaction checkpoint was not saved")
	}
	checkpoint, found, err := loadContextCompactionCheckpoint(ctx, original)
	if err != nil {
		t.Fatalf("loadContextCompactionCheckpoint() error = %v", err)
	}
	if !found {
		t.Fatal("saved compaction checkpoint was not reusable")
	}
	if checkpoint.PrefixContentCount <= 0 ||
		checkpoint.PrefixContentCount > len(original) {
		t.Fatalf(
			"checkpoint prefix count = %d, want a non-empty valid prefix",
			checkpoint.PrefixContentCount,
		)
	}
	if len(request.Contents) >= len(original) {
		t.Fatalf(
			"compacted contents length = %d, want less than %d",
			len(request.Contents),
			len(original),
		)
	}
	if !strings.Contains(
		request.Contents[0].Parts[0].Text,
		"Durable compacted context.",
	) {
		t.Fatalf("compacted request does not contain the generated summary")
	}

	appended := genai.NewContentFromText("Continue with the next task.", genai.RoleUser)
	nextOriginal := append(append([]*genai.Content(nil), original...), appended)
	nextRequest := &model.LLMRequest{Model: "test-model", Contents: nextOriginal}
	if _, err := compactor.beforeModel(ctx, nextRequest); err != nil {
		t.Fatalf("beforeModel() with checkpoint error = %v", err)
	}
	if summarizer.calls != callsAfterCompaction {
		t.Fatalf(
			"summarizer calls after checkpoint reuse = %d, want %d",
			summarizer.calls,
			callsAfterCompaction,
		)
	}
	if len(updates) != 2 {
		t.Fatalf("checkpoint reuse emitted updates = %#v", updates)
	}
	if len(nextRequest.Contents) == 0 ||
		!strings.Contains(
			nextRequest.Contents[0].Parts[0].Text,
			"Durable compacted context.",
		) {
		t.Fatal("checkpoint summary was not applied to the next request")
	}
	lastContent := nextRequest.Contents[len(nextRequest.Contents)-1]
	if len(lastContent.Parts) == 0 ||
		lastContent.Parts[len(lastContent.Parts)-1] != appended.Parts[0] {
		t.Fatal("recent content was not preserved after checkpoint reuse")
	}
}

func TestContextCompactorRecompactionTracksMergedSummaryPrefix(t *testing.T) {
	t.Parallel()

	summarizer := &compactionTestModel{summary: "Updated durable context."}
	compactor := newContextCompactor(summarizer, 30_000, 0)
	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-with-merged-summary",
	}
	const toolCallID = "call-large-result"
	original := []*genai.Content{
		genai.NewContentFromText("Previously compacted turn.", genai.RoleUser),
		genai.NewContentFromText("Inspect the latest implementation.", genai.RoleUser),
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   toolCallID,
					Name: "read_file",
					Args: map[string]any{"path": "large.go"},
				},
			}},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{
					ID:   toolCallID,
					Name: "read_file",
					Response: map[string]any{
						"content": strings.Repeat("x", 120_000),
					},
				},
			}},
		},
		genai.NewContentFromText("Continue with the current task.", genai.RoleUser),
	}
	digest, err := contentDigest(original[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := saveContextCompactionCheckpoint(ctx, contextCompactionCheckpoint{
		Version:            1,
		PrefixContentCount: 1,
		PrefixDigest:       digest,
		Summary:            "Existing durable context.",
	}); err != nil {
		t.Fatal(err)
	}

	request := &model.LLMRequest{Model: "test-model", Contents: original}
	if _, err := compactor.beforeModel(ctx, request); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	checkpoint, found, err := loadContextCompactionCheckpoint(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	if !found || checkpoint.PrefixContentCount != 4 {
		t.Fatalf("checkpoint = %#v, found = %v; want prefix content count 4", checkpoint, found)
	}
	for _, content := range request.Contents {
		if contentHasFunctionResponse(content) {
			t.Fatal("compacted request retained a tool result whose call was summarized")
		}
	}
	if len(request.Contents) != 1 || len(request.Contents[0].Parts) != 2 ||
		!strings.Contains(request.Contents[0].Parts[0].Text, "Updated durable context.") ||
		request.Contents[0].Parts[1].Text != "Continue with the current task." {
		t.Fatalf("compacted contents = %#v", request.Contents)
	}
}

func TestContextCompactionTranscriptItem(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.August, 3, 9, 0, 0, 0, time.UTC)
	update := contextCompactionUpdate{
		ID:                    "context-compaction:invocation-1:8",
		Status:                "completed",
		EstimatedTokensBefore: 115_000,
		EstimatedTokensAfter:  36_000,
		MaxContextTokens:      128_000,
		SummarizedContents:    8,
	}
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			CustomMetadata: map[string]any{contextCompactionMetadataKey: update},
		},
		ID:           update.ID,
		Timestamp:    createdAt,
		InvocationID: "invocation-1",
		Author:       "workspace_agent",
	}
	item, ok := contextCompactionTranscriptItem(event, store.Run{
		APICompatibility: "anthropic",
		ModelID:          "claude-test",
	})
	if !ok {
		t.Fatal("contextCompactionTranscriptItem() did not recognize the metadata event")
	}
	if item.Kind != "context_compaction" ||
		item.InvocationID != "invocation-1" ||
		item.ToolInput["estimatedTokensBefore"] != int64(115_000) ||
		item.ToolOutput["state"] != "completed" ||
		item.ToolOutput["estimatedTokensAfter"] != int64(36_000) ||
		item.CreatedAt != createdAt {
		t.Fatalf("contextCompactionTranscriptItem() = %#v", item)
	}
}

func TestSafeContextCompactionBoundaryKeepsToolExchangeTogether(t *testing.T) {
	t.Parallel()

	contents := []*genai.Content{
		genai.NewContentFromText("Inspect the workspace.", genai.RoleUser),
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "read_file",
					Args: map[string]any{"path": "main.go"},
				},
			}},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{
					ID:       "call-1",
					Name:     "read_file",
					Response: map[string]any{"output": "package main"},
				},
			}},
		},
		genai.NewContentFromText("The file contains a main package.", genai.RoleModel),
	}

	if safeContextCompactionBoundary(contents, 2) {
		t.Fatal("boundary between a tool call and its result was considered safe")
	}
	if !safeContextCompactionBoundary(contents, 1) {
		t.Fatal("boundary before a complete tool exchange was considered unsafe")
	}
	if !safeContextCompactionBoundary(contents, 3) {
		t.Fatal("boundary after a complete tool exchange was considered unsafe")
	}
}

func TestContextCompactorClassifiesContextLengthErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "openai context length",
			err: errors.New(
				"This model's maximum context length is 128000 tokens. " +
					"However, you requested 200000 tokens in total",
			),
			want: true,
		},
		{
			name: "anthropic prompt too long",
			err: errors.New(
				"prompt is too long: 250000 tokens is more than " +
					"model's maximum of 200000 tokens",
			),
			want: true,
		},
		{
			name: "qwen input budget",
			err: errors.New(
				"InvalidParameter: Range of input + max_tokens " +
					"length should be [1, 258048]",
			),
			want: true,
		},
		{
			name: "generic context window",
			err:  errors.New("request exceeds the context window limit"),
			want: true,
		},
		{
			name: "max_tokens validation",
			err: errors.New(
				"Invalid value for 'max_tokens': must be between 1 and 32768",
			),
			want: false,
		},
		{
			name: "unsupported max_tokens parameter",
			err: errors.New(
				"Unsupported parameter: 'max_tokens'. " +
					"Use 'max_completion_tokens' instead.",
			),
			want: false,
		},
		{
			name: "plan quota",
			err: errors.New(
				"You have reached the token limit for your plan this month.",
			),
			want: false,
		},
		{
			name: "server error",
			err:  errors.New("internal server error"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isContextLengthError(tc.err); got != tc.want {
				t.Fatalf(
					"isContextLengthError(%v) = %v, want %v",
					tc.err, got, tc.want,
				)
			}
		})
	}
}

func TestContextCompactorFallsBackWhenSummarizerFails(t *testing.T) {
	t.Parallel()

	summarizer := &compactionTestModel{err: errors.New("summarizer exploded")}
	compactor := newContextCompactor(summarizer, 30_000, 6_000)
	var updates []contextCompactionUpdate
	compactor.onUpdate = func(_ agent.Context, update contextCompactionUpdate) {
		updates = append(updates, update)
	}
	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-summarizer-failure",
	}
	original := []*genai.Content{
		genai.NewContentFromText(strings.Repeat("a", 23_000), genai.RoleUser),
		genai.NewContentFromText(strings.Repeat("b", 23_000), genai.RoleModel),
		genai.NewContentFromText(strings.Repeat("c", 23_000), genai.RoleUser),
		genai.NewContentFromText(strings.Repeat("d", 23_000), genai.RoleModel),
	}
	request := &model.LLMRequest{Model: "test-model", Contents: original}

	// A summarizer failure must not fail the run: the prefix is dropped
	// with the fallback summary so the session can continue below the
	// window instead of re-failing on the same oversized request.
	if _, err := compactor.beforeModel(ctx, request); err != nil {
		t.Fatalf("beforeModel() error = %v, want nil on summarizer failure", err)
	}
	if summarizer.calls == 0 {
		t.Fatal("summarizer was not called above the compaction threshold")
	}
	if len(updates) != 2 || updates[1].Status != "completed" || updates[1].Error == "" {
		t.Fatalf(
			"compaction updates = %#v, want completed with surfaced summarizer error",
			updates,
		)
	}
	if _, ok := state[contextCompactionStateKey].(string); !ok {
		t.Fatal("compaction checkpoint was not saved after fallback")
	}
	first := request.Contents[0]
	if len(first.Parts) == 0 ||
		!strings.Contains(first.Parts[0].Text, contextCompactionFallbackSummary) {
		t.Fatalf("compacted request does not carry the fallback summary: %#v", first)
	}
}

func TestContextCompactorMeasuredProjectionSurvivesCompaction(t *testing.T) {
	t.Parallel()

	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-measured-projection",
	}
	original := []*genai.Content{
		genai.NewContentFromText("First durable turn.", genai.RoleUser),
		genai.NewContentFromText("Second turn.", genai.RoleModel),
		genai.NewContentFromText("Third turn.", genai.RoleUser),
		genai.NewContentFromText("Fourth turn.", genai.RoleModel),
		genai.NewContentFromText("Fifth turn.", genai.RoleUser),
	}
	digest, err := contentDigest(original[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err := saveContextCompactionCheckpoint(ctx, contextCompactionCheckpoint{
		Version:            1,
		PrefixContentCount: 2,
		PrefixDigest:       digest,
		Summary:            "Durable compacted context.",
	}); err != nil {
		t.Fatal(err)
	}

	// The previous run sent the checkpoint-applied request and the provider
	// measured its prompt size.
	sentCompactor := newContextCompactor(
		&compactionTestModel{summary: "unused"},
		100_000,
		1_000,
	)
	base := sentCompactor.checkpointedContents(ctx, original)
	// The summary merges into the first retained user content, so the sent
	// request has one fewer content than the full history.
	if len(base) != len(original)-2 {
		t.Fatalf("checkpointed contents = %d, want %d", len(base), len(original)-2)
	}
	sentCompactor.recordSentRequest(base)
	if _, err := sentCompactor.afterModel(
		ctx,
		&model.LLMResponse{
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: 42_000,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("afterModel() error = %v", err)
	}

	// A later run (fresh compactor, e.g. after a process restart) appends
	// new content. The measured baseline must project from the sent,
	// checkpoint-applied contents, not the full un-compacted history.
	appended := genai.NewContentFromText("Sixth turn with fresh context.", genai.RoleUser)
	next := append(append([]*genai.Content(nil), original...), appended)
	compactor := newContextCompactor(&compactionTestModel{summary: "unused"}, 100_000, 1_000)

	delta, err := estimateJSONTokens(appended)
	if err != nil {
		t.Fatal(err)
	}
	projected := compactor.projectedInputTokens(
		ctx,
		compactor.checkpointedContents(ctx, next),
	)
	if want := int64(42_000) + delta; projected != want {
		t.Fatalf("projectedInputTokens() = %d, want %d", projected, want)
	}
	if stale := compactor.projectedInputTokens(ctx, next); stale != 0 {
		t.Fatalf(
			"projectedInputTokens() against un-compacted contents = %d, want 0",
			stale,
		)
	}
}

func TestContextCompactorKeepsSummaryAcrossEmptyBatch(t *testing.T) {
	t.Parallel()

	summarizer := &compactionTestModel{
		summary: "Later batch summary.",
		summaries: []string{
			"First batch summary.",
			"",
			"Third batch summary.",
		},
	}
	compactor := newContextCompactor(summarizer, 10_000, 1_000)
	state := compactionTestState{}
	ctx := &compactionTestContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		sessionID:         "session-empty-batch",
	}
	content := genai.NewContentFromText(strings.Repeat("a", 60_000), genai.RoleUser)
	request := &model.LLMRequest{Model: "test-model", Contents: []*genai.Content{content}}

	if _, err := compactor.beforeModel(ctx, request); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	if len(summarizer.prompts) < 3 {
		t.Fatalf("summarizer batches = %d, want at least 3", len(summarizer.prompts))
	}
	// A batch that returns no visible text must not discard the summary
	// accumulated from earlier batches.
	if !strings.Contains(summarizer.prompts[2], "First batch summary.") {
		t.Fatalf(
			"third batch prompt lost the accumulated summary:\n%s",
			summarizer.prompts[2],
		)
	}
}

type compactionTestModel struct {
	calls     int
	summary   string
	summaries []string
	err       error
	prompts   []string
}

func (m *compactionTestModel) Name() string {
	return "compaction-test-model"
}

func (m *compactionTestModel) GenerateContent(
	_ context.Context,
	request *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if request != nil &&
			len(request.Contents) > 0 &&
			request.Contents[0] != nil &&
			len(request.Contents[0].Parts) > 0 &&
			request.Contents[0].Parts[0] != nil {
			m.prompts = append(m.prompts, request.Contents[0].Parts[0].Text)
		}
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		text := m.summary
		if m.calls <= len(m.summaries) {
			text = m.summaries[m.calls-1]
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleModel),
		}, nil)
	}
}

type compactionTestState map[string]any

func (s compactionTestState) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s compactionTestState) Set(key string, value any) error {
	s[key] = value
	return nil
}

func (s compactionTestState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range s {
			if !yield(key, value) {
				return
			}
		}
	}
}

type compactionTestContext struct {
	agent.StrictContextMock
	state     session.State
	sessionID string
}

func (c *compactionTestContext) State() session.State {
	return c.state
}

func (c *compactionTestContext) SessionID() string {
	return c.sessionID
}

func (c *compactionTestContext) InvocationID() string {
	return "compaction-test-invocation"
}
