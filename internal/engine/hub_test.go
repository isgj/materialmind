package engine

import (
	"context"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

func TestHubReplaysAndCompletes(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	hub.Publish("run-1", "message_delta", map[string]string{"text": "one"})
	hub.Publish("run-1", "message_delta", map[string]string{"text": "two"})
	hub.Complete("run-1")

	events, ok := hub.Subscribe(context.Background(), "run-1", 1)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	event, open := <-events
	if !open || event.Sequence != 2 || event.Type != "message_delta" {
		t.Fatalf("event = %#v, open = %v", event, open)
	}
	if _, open := <-events; open {
		t.Fatal("completed subscription remains open")
	}
}

func TestHubEvictsCompletedStream(t *testing.T) {
	hub := newHub(time.Millisecond)
	hub.Create("run-1")
	hub.Publish("run-1", "done", map[string]string{"status": "completed"})
	hub.Complete("run-1")

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := hub.Subscribe(context.Background(), "run-1", 0); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Subscribe() found an expired completed stream")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHubBoundsReplayAndReportsTruncation(t *testing.T) {
	hub := newHubWithLimits(time.Minute, 3, 1)
	hub.Create("run-1")
	for sequence := 1; sequence <= 5; sequence++ {
		hub.Publish("run-1", "message_delta", sequence)
	}

	subscription, ok := hub.SubscribeReplay(t.Context(), "run-1", 1)
	if !ok {
		t.Fatal("SubscribeReplay() ok = false")
	}
	if !subscription.Truncated || subscription.OldestSequence != 3 {
		t.Fatalf("subscription = %#v", subscription)
	}
	for want := int64(3); want <= 5; want++ {
		event := <-subscription.Events
		if event.Sequence != want {
			t.Fatalf("event sequence = %d, want %d", event.Sequence, want)
		}
	}
}

func TestHubDisconnectsSlowSubscriber(t *testing.T) {
	hub := newHubWithLimits(time.Minute, 3, 1)
	hub.Create("run-1")
	events, ok := hub.Subscribe(t.Context(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	hub.Publish("run-1", "message_delta", "one")
	hub.Publish("run-1", "message_delta", "two")

	first, open := <-events
	if !open || first.Sequence != 1 {
		t.Fatalf("first event = %#v, open = %v", first, open)
	}
	if _, open := <-events; open {
		t.Fatal("slow subscriber remains connected")
	}
}

func TestPublishEventDoesNotStreamUserTextAsAssistantText(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	engine := &Engine{hub: hub}
	run := store.Run{ID: "run-1"}
	engine.publishEvent(run, &session.Event{
		Author: "user",
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText("user text", genai.RoleUser),
		},
	})
	engine.publishEvent(run, &session.Event{
		Author: "workspace_agent",
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText("assistant text", genai.RoleModel),
		},
	})
	hub.Complete("run-1")

	events, ok := hub.Subscribe(context.Background(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	event, open := <-events
	if !open || event.Type != "message_complete" {
		t.Fatalf("event = %#v, open = %v", event, open)
	}
	if _, open := <-events; open {
		t.Fatal("user text produced an additional stream event")
	}
}

func TestPublishEventStreamsSubAgentLifecycleAndChildMetadata(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	engine := &Engine{hub: hub}
	run := store.Run{ID: "run-1"}

	engine.publishEvent(run, &session.Event{
		Author: "workspace_agent",
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID: "delegation-1", Name: "workspace_explorer",
				Args: map[string]any{"request": "Locate the session store."},
			},
		}}}},
	})
	engine.publishEvent(run, &session.Event{
		ID:             "child-message",
		Author:         "workspace_explorer",
		Branch:         "workspace_agent.workspace_explorer",
		IsolationScope: "delegation-1",
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText("I will inspect the store.", genai.RoleModel),
		},
	})
	engine.publishEvent(run, &session.Event{
		Author:         "workspace_explorer",
		Branch:         "workspace_agent.workspace_explorer",
		IsolationScope: "delegation-1",
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID: "read-1", Name: "read_file", Args: map[string]any{"path": "internal/store/store.go"},
			},
		}}}},
	})
	engine.publishEvent(run, &session.Event{
		Author:         "workspace_explorer",
		IsolationScope: "delegation-1",
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID: "finish-1", Name: finishTaskToolName, Args: map[string]any{"result": "Found it."},
			},
		}}}},
	})
	profile, ok := subAgentProfileForName("workspace_explorer")
	if !ok {
		t.Fatal("workspace explorer profile not found")
	}
	engine.publishSubAgentCompletion("run-1", "delegation-1", profile, "Found it.", nil)
	hub.Complete("run-1")

	events, ok := hub.Subscribe(context.Background(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	var collected []StreamEvent
	for event := range events {
		collected = append(collected, event)
	}
	if len(collected) != 4 {
		t.Fatalf("stream events = %#v, want 4 events", collected)
	}
	wantTypes := []string{"subagent_started", "message_complete", "tool_call", "subagent_completed"}
	for index, want := range wantTypes {
		if collected[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, collected[index].Type, want)
		}
	}
	childPayload, ok := collected[2].Data.(map[string]any)
	if !ok {
		t.Fatalf("child tool payload = %T, want map[string]any", collected[2].Data)
	}
	if childPayload["agentName"] != "workspace_explorer" ||
		childPayload["agentLabel"] != "Workspace explorer" ||
		childPayload["delegationId"] != "delegation-1" {
		t.Fatalf("child tool payload = %#v", childPayload)
	}
	completionPayload, ok := collected[3].Data.(map[string]any)
	if !ok {
		t.Fatalf("subagent completion payload = %T, want map[string]any", collected[3].Data)
	}
	if completionPayload["id"] != "delegation-1" {
		t.Fatalf("subagent completion payload = %#v", completionPayload)
	}
}
