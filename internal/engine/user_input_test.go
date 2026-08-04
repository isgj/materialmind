package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"materialmind/internal/store"
	"materialmind/internal/workspacetools"
)

func TestResolveUserInputAnswersPreservesQuestionOrder(t *testing.T) {
	questions := []workspacetools.AskUserQuestion{
		{
			ID:       "target",
			Question: "Where should this run?",
			Options: []workspacetools.AskUserOption{
				{ID: "local", Label: "Local"},
				{ID: "remote", Label: "Remote"},
			},
		},
		{ID: "name", Question: "What should it be called?"},
	}
	answers, err := resolveUserInputAnswers(questions, []UserInputAnswerSubmission{
		{QuestionID: "name", Text: "Deployment check"},
		{QuestionID: "target", OptionID: "remote"},
	})
	if err != nil {
		t.Fatalf("resolveUserInputAnswers() error = %v", err)
	}
	if len(answers) != 2 ||
		answers[0].QuestionID != "target" ||
		answers[0].OptionID != "remote" ||
		answers[0].Answer != "Remote" ||
		answers[1].QuestionID != "name" ||
		answers[1].Answer != "Deployment check" {
		t.Fatalf("resolveUserInputAnswers() = %#v", answers)
	}
}

func TestResolveUserInputAnswersRejectsInvalidResponses(t *testing.T) {
	questions := []workspacetools.AskUserQuestion{{
		ID:       "target",
		Question: "Where should this run?",
		Options:  []workspacetools.AskUserOption{{ID: "local", Label: "Local"}},
	}}
	tests := []struct {
		name        string
		submissions []UserInputAnswerSubmission
		wantError   string
	}{
		{name: "missing", wantError: "every question"},
		{
			name: "unknown option",
			submissions: []UserInputAnswerSubmission{{
				QuestionID: "target",
				OptionID:   "remote",
			}},
			wantError: "not available",
		},
		{
			name: "option and text",
			submissions: []UserInputAnswerSubmission{{
				QuestionID: "target",
				OptionID:   "local",
				Text:       "Something else",
			}},
			wantError: "either an option",
		},
		{
			name: "empty",
			submissions: []UserInputAnswerSubmission{{
				QuestionID: "target",
			}},
			wantError: "requires an answer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveUserInputAnswers(questions, test.submissions)
			if err == nil ||
				!strings.Contains(err.Error(), test.wantError) ||
				!strings.Contains(err.Error(), store.ErrInvalidInput.Error()) {
				t.Fatalf(
					"resolveUserInputAnswers() error = %v, want invalid input containing %q",
					err,
					test.wantError,
				)
			}
		})
	}
}

func TestUserInputRequestWaitsForResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runEngine, runRecord := newUserInputTestEngine(t, ctx)
	events, ok := runEngine.Hub().Subscribe(ctx, runRecord.ID, 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	answers := make(chan []workspacetools.AskUserAnswer, 1)
	errorResults := make(chan error, 1)
	go func() {
		result, err := runEngine.requestUserInput(
			ctx,
			runRecord.SessionID,
			runRecord.ID,
			"tool-call-1",
			[]workspacetools.AskUserQuestion{{
				ID:       "target",
				Question: "Where should this run?",
				Options:  []workspacetools.AskUserOption{{ID: "local", Label: "Local"}},
			}},
		)
		answers <- result
		errorResults <- err
	}()

	event := <-events
	if event.Type != "user_input_request" {
		t.Fatalf("event.Type = %q, want user_input_request", event.Type)
	}
	request, ok := event.Data.(UserInputRequest)
	if !ok || request.ToolCallID != "tool-call-1" || len(request.Questions) != 1 {
		t.Fatalf("event.Data = %#v", event.Data)
	}
	if !runEngine.WaitingForUser(runRecord.SessionID) {
		t.Fatal("WaitingForUser() = false while request is pending")
	}

	resolution, err := runEngine.ResolveUserInput(
		ctx,
		runRecord.ID,
		request.ID,
		[]UserInputAnswerSubmission{{QuestionID: "target", OptionID: "local"}},
	)
	if err != nil {
		t.Fatalf("ResolveUserInput() error = %v", err)
	}
	if len(resolution.Answers) != 1 || resolution.Answers[0].Answer != "Local" {
		t.Fatalf("ResolveUserInput() = %#v", resolution)
	}
	if err := <-errorResults; err != nil {
		t.Fatalf("requestUserInput() error = %v", err)
	}
	if result := <-answers; len(result) != 1 || result[0].OptionID != "local" {
		t.Fatalf("requestUserInput() = %#v", result)
	}
	if runEngine.WaitingForUser(runRecord.SessionID) {
		t.Fatal("WaitingForUser() = true after resolution")
	}
	if _, err := runEngine.ResolveUserInput(
		ctx,
		runRecord.ID,
		request.ID,
		[]UserInputAnswerSubmission{{QuestionID: "target", OptionID: "local"}},
	); !errors.Is(err, ErrUserInputNotPending) {
		t.Fatalf("ResolveUserInput() duplicate error = %v, want ErrUserInputNotPending", err)
	}
}

func TestUserInputRequestStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runEngine, runRecord := newUserInputTestEngine(t, ctx)
	events, ok := runEngine.Hub().Subscribe(context.Background(), runRecord.ID, 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	result := make(chan error, 1)
	go func() {
		_, err := runEngine.requestUserInput(
			ctx,
			runRecord.SessionID,
			runRecord.ID,
			"tool-call-1",
			[]workspacetools.AskUserQuestion{{ID: "target", Question: "Where?"}},
		)
		result <- err
	}()
	<-events
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("requestUserInput() error = %v, want context.Canceled", err)
	}
	if runEngine.WaitingForUser(runRecord.SessionID) {
		t.Fatal("WaitingForUser() = true after cancellation")
	}
}

func newUserInputTestEngine(
	t *testing.T,
	ctx context.Context,
) (*Engine, store.Run) {
	t.Helper()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(
		ctx,
		"Claude",
		"anthropic",
		"https://example.test",
		"",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	modelRecord, err := dataStore.CreateLLMModel(
		ctx,
		provider.ID,
		"Claude",
		"claude-test",
		store.GenerationSettings{MaxOutputTokens: 4096},
	)
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}
	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Questions", &modelRecord.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	runRecord, err := dataStore.CreateRun(
		ctx,
		sessionRecord.ID,
		modelRecord.ID,
		"Ask me",
		store.RunGenerationOverrides{},
	)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	runEngine := New(dataStore)
	runEngine.active[sessionRecord.ID] = &activeRun{
		runID:             runRecord.ID,
		pendingApprovals:  make(map[string]*pendingToolApproval),
		pendingUserInputs: make(map[string]*pendingUserInput),
	}
	runEngine.hub.Create(runRecord.ID)
	return runEngine, runRecord
}
