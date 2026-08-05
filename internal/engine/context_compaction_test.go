package engine

import (
	"context"
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

type compactionTestModel struct {
	calls   int
	summary string
}

func (m *compactionTestModel) Name() string {
	return "compaction-test-model"
}

func (m *compactionTestModel) GenerateContent(
	_ context.Context,
	_ *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText(m.summary, genai.RoleModel),
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
