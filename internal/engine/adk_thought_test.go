package engine

import (
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

func TestTranscriptClassifiesADKThoughtAsAgentNote(t *testing.T) {
	ctx := t.Context()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()

	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(
		ctx,
		"OpenAI Responses",
		"openai-responses",
		"https://responses.example.test/v1",
		"",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	modelRecord, err := dataStore.CreateLLMModel(
		ctx,
		provider.ID,
		"Reasoning model",
		"reasoning-test",
		store.GenerationSettings{MaxOutputTokens: 4096},
	)
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}

	runEngine := New(dataStore)
	sessionRecord, err := runEngine.CreateSession(
		ctx,
		workspace.ID,
		"Reasoning session",
		&modelRecord.ID,
	)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	runRecord, err := dataStore.CreateRun(
		ctx,
		sessionRecord.ID,
		modelRecord.ID,
		"Inspect the workspace.",
		store.RunGenerationOverrides{},
	)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	const invocationID = "invocation-1"
	if _, err := dataStore.UpdateRun(
		ctx,
		runRecord.ID,
		"completed",
		invocationID,
		"",
	); err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}

	createdAt := time.Date(2026, time.August, 10, 10, 0, 0, 0, time.UTC)
	if err := runEngine.sessionService.AppendTranscriptEvent(
		ctx,
		AppName,
		UserID,
		sessionRecord.ID,
		&session.Event{
			ID:           "response-1",
			InvocationID: invocationID,
			Author:       "workspace_agent",
			Timestamp:    createdAt,
			LLMResponse: model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{Thought: true, ThoughtSignature: []byte("opaque")},
					{Text: "Inspecting the workspace.", Thought: true},
					{Text: "The workspace is ready."},
				},
			}},
		},
	); err != nil {
		t.Fatalf("AppendTranscriptEvent() error = %v", err)
	}

	transcript, err := runEngine.Transcript(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("Transcript() error = %v", err)
	}
	if len(transcript) != 2 {
		t.Fatalf("Transcript() = %#v, want 2 visible items", transcript)
	}
	thought := transcript[0]
	if thought.Kind != "thought" ||
		thought.Role != "assistant" ||
		thought.Text != "Inspecting the workspace." ||
		thought.Provider != "openai-responses" ||
		thought.Model != "reasoning-test" ||
		thought.CreatedAt != createdAt {
		t.Fatalf("thought transcript item = %#v", thought)
	}
	message := transcript[1]
	if message.Kind != "message" ||
		message.Role != "assistant" ||
		message.Text != "The workspace is ready." {
		t.Fatalf("assistant transcript item = %#v", message)
	}
}
