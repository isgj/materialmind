package adksession_test

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
	"google.golang.org/genai"

	"materialmind/internal/adksession"
	"materialmind/internal/store"
)

func TestServiceConformance(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
	}, func(t *testing.T) session.Service {
		dataStore, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "materialmind.db"))
		if err != nil {
			t.Fatalf("store.Open() error = %v", err)
		}
		t.Cleanup(func() { dataStore.Close() })
		return adksession.New(dataStore.DB())
	})
}

func TestServicePersistsEventsAndScopedState(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()
	service := adksession.New(dataStore.DB())

	created, err := service.Create(ctx, &session.CreateRequest{
		AppName: "test-app", UserID: "test-user", SessionID: "session-1",
		State: map[string]any{
			"session-value":                "one",
			session.KeyPrefixApp + "app":   "shared-app",
			session.KeyPrefixUser + "user": "shared-user",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := session.NewEvent(ctx, "invocation-1")
	event.Author = "workspace_agent"
	event.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText("Hello", genai.RoleModel),
	}
	event.Actions.StateDelta = map[string]any{"turn": float64(1)}
	if err := service.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	loaded, err := service.Get(ctx, &session.GetRequest{
		AppName: "test-app", UserID: "test-user", SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Session.Events().Len() != 1 {
		t.Fatalf("Events().Len() = %d, want 1", loaded.Session.Events().Len())
	}
	if got := loaded.Session.Events().At(0).Content.Parts[0].Text; got != "Hello" {
		t.Fatalf("event text = %q, want Hello", got)
	}
	if got, err := loaded.Session.State().Get("turn"); err != nil || got != float64(1) {
		t.Fatalf("session state turn = %#v, %v", got, err)
	}

	second, err := service.Create(ctx, &session.CreateRequest{
		AppName: "test-app", UserID: "test-user", SessionID: "session-2",
	})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	if got, err := second.Session.State().Get(session.KeyPrefixApp + "app"); err != nil || got != "shared-app" {
		t.Fatalf("app state = %#v, %v", got, err)
	}
	if got, err := second.Session.State().Get(session.KeyPrefixUser + "user"); err != nil || got != "shared-user" {
		t.Fatalf("user state = %#v, %v", got, err)
	}
}

func TestServiceIgnoresPartialEvents(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()
	service := adksession.New(dataStore.DB())
	created, err := service.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := session.NewEvent(ctx, "invocation")
	event.Partial = true
	if err := service.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if created.Session.Events().Len() != 0 {
		t.Fatalf("Events().Len() = %d, want 0", created.Session.Events().Len())
	}
}

func TestAppendTranscriptEventDoesNotAdvanceSessionVersion(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()
	service := adksession.New(dataStore.DB())
	created, err := service.Create(ctx, &session.CreateRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	childEvent := session.NewEvent(ctx, "invocation")
	childEvent.Author = "workspace_explorer"
	childEvent.IsolationScope = "delegation-1"
	childEvent.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText("Child report", genai.RoleModel),
	}
	if err := service.AppendTranscriptEvent(
		ctx,
		"app",
		"user",
		"session",
		childEvent,
	); err != nil {
		t.Fatalf("AppendTranscriptEvent() error = %v", err)
	}

	parentEvent := session.NewEvent(ctx, "invocation")
	parentEvent.Author = "workspace_agent"
	parentEvent.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText("Parent response", genai.RoleModel),
	}
	if err := service.AppendEvent(ctx, created.Session, parentEvent); err != nil {
		t.Fatalf("AppendEvent() after transcript event error = %v", err)
	}

	loaded, err := service.Get(ctx, &session.GetRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Session.Events().Len() != 2 {
		t.Fatalf("Events().Len() = %d, want 2", loaded.Session.Events().Len())
	}
	if got := loaded.Session.Events().At(0).IsolationScope; got != "delegation-1" {
		t.Fatalf("child IsolationScope = %q, want delegation-1", got)
	}

	runnerLoaded, err := service.RunnerService().Get(ctx, &session.GetRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("RunnerService().Get() error = %v", err)
	}
	if runnerLoaded.Session.Events().Len() != 1 {
		t.Fatalf(
			"runner Events().Len() = %d, want 1",
			runnerLoaded.Session.Events().Len(),
		)
	}
	if got := runnerLoaded.Session.Events().At(0).Content.Parts[0].Text; got != "Parent response" {
		t.Fatalf("runner event text = %q, want Parent response", got)
	}
}

func TestRunnerServiceRepairsInterruptedToolCalls(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()
	service := adksession.New(dataStore.DB())
	created, err := service.Create(ctx, &session.CreateRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	callEvent := session.NewEvent(ctx, "invocation")
	callEvent.Author = "workspace_agent"
	callEvent.LLMResponse = model.LLMResponse{Content: &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "delegation-1", Name: "code_reviewer",
			Args: map[string]any{"request": "Review the change."},
		}}},
	}}
	if err := service.AppendEvent(ctx, created.Session, callEvent); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	childEvent := session.NewEvent(ctx, "invocation")
	childEvent.Author = "code_reviewer"
	childEvent.IsolationScope = "delegation-1"
	childEvent.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText("Still reviewing.", genai.RoleModel),
	}
	if err := service.AppendTranscriptEvent(
		ctx,
		"app",
		"user",
		"session",
		childEvent,
	); err != nil {
		t.Fatalf("AppendTranscriptEvent() error = %v", err)
	}

	runnerService := service.RunnerService()
	runnerLoaded, err := runnerService.Get(ctx, &session.GetRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("RunnerService().Get() error = %v", err)
	}
	if runnerLoaded.Session.Events().Len() != 2 {
		t.Fatalf(
			"runner Events().Len() = %d, want call and repair",
			runnerLoaded.Session.Events().Len(),
		)
	}
	repair := runnerLoaded.Session.Events().At(1)
	if repair.Content == nil || len(repair.Content.Parts) != 1 {
		t.Fatalf("repair event = %#v", repair)
	}
	response := repair.Content.Parts[0].FunctionResponse
	if response == nil || response.ID != "delegation-1" ||
		response.Name != "code_reviewer" ||
		response.Response["error"] != "tool call was interrupted before completion" {
		t.Fatalf("repair response = %#v", response)
	}

	followUp := session.NewEvent(ctx, "follow-up")
	followUp.Author = "user"
	followUp.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText("Continue.", genai.RoleUser),
	}
	if err := runnerService.AppendEvent(ctx, runnerLoaded.Session, followUp); err != nil {
		t.Fatalf("runner AppendEvent() error = %v", err)
	}
	persisted, err := service.Get(ctx, &session.GetRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if persisted.Session.Events().Len() != 3 {
		t.Fatalf(
			"persisted Events().Len() = %d, want three without repair",
			persisted.Session.Events().Len(),
		)
	}
	reloaded, err := runnerService.Get(ctx, &session.GetRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatalf("second RunnerService().Get() error = %v", err)
	}
	if reloaded.Session.Events().Len() != 2 {
		t.Fatalf(
			"reloaded runner Events().Len() = %d, want call and follow-up",
			reloaded.Session.Events().Len(),
		)
	}
	followUpContent := reloaded.Session.Events().At(1).Content
	if followUpContent == nil || len(followUpContent.Parts) != 2 ||
		followUpContent.Parts[0].FunctionResponse == nil ||
		followUpContent.Parts[1].Text != "Continue." {
		t.Fatalf("repaired follow-up content = %#v", followUpContent)
	}
}
