package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"materialmind/internal/agentskills"
	"materialmind/internal/store"
)

func TestDelegationToolsRunSubAgentsConcurrentlyAndPersistChildEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer dataStore.Close()
	engine := New(dataStore)
	engine.hub.Create("run-1")
	if _, err := engine.sessionService.Create(ctx, &session.CreateRequest{
		AppName: AppName, UserID: UserID, SessionID: "session-1",
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	scriptedModel := newParallelSubAgentModel()
	runRecord := store.Run{ID: "run-1", SessionID: "session-1"}
	delegationTools, err := engine.newSubAgentTools(
		scriptedModel,
		runRecord,
		store.Workspace{RootPath: t.TempDir()},
		nil,
		agentskills.Catalog{},
		nil,
	)
	if err != nil {
		t.Fatalf("newSubAgentTools() error = %v", err)
	}
	assertDelegationToolSchema(t, delegationTools[0])
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "workspace_agent",
		Model:       scriptedModel,
		Mode:        llmagent.ModeChat,
		Instruction: "Coordinate the requested specialist reviews.",
		Tools:       delegationTools[:2],
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	rootRunner, err := runner.New(runner.Config{
		AppName: AppName, Agent: rootAgent,
		SessionService: engine.sessionService.RunnerService(),
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	for _, runErr := range rootRunner.Run(
		ctx,
		UserID,
		"session-1",
		genai.NewContentFromText("Review this change.", genai.RoleUser),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	for _, runErr := range rootRunner.Run(
		ctx,
		UserID,
		"session-1",
		genai.NewContentFromText("Follow up on the review.", genai.RoleUser),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	) {
		if runErr != nil {
			t.Fatalf("follow-up Run() error = %v", runErr)
		}
	}
	if scriptedModel.maxConcurrentChildren() < 2 {
		t.Fatalf(
			"maximum concurrent child calls = %d, want at least 2",
			scriptedModel.maxConcurrentChildren(),
		)
	}
	if !scriptedModel.observedFollowUp() {
		t.Fatal("coordinator did not receive the follow-up user turn")
	}
	engine.hub.mu.Lock()
	streamEvents := append([]StreamEvent(nil), engine.hub.streams["run-1"].events...)
	engine.hub.mu.Unlock()
	completions := make(map[string]int)
	for _, event := range streamEvents {
		if event.Type != "subagent_completed" {
			continue
		}
		payload, ok := event.Data.(map[string]any)
		if !ok {
			t.Fatalf("subagent completion payload = %T, want map[string]any", event.Data)
		}
		delegationID, _ := payload["id"].(string)
		completions[delegationID]++
	}
	if completions["delegation-1"] != 1 || completions["delegation-2"] != 1 {
		t.Fatalf("subagent completion counts = %#v, want one per delegation", completions)
	}

	loaded, err := engine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	childEvents := map[string]int{}
	for event := range loaded.Session.Events().All() {
		if event != nil && event.IsolationScope != "" {
			childEvents[event.IsolationScope]++
		}
	}
	if childEvents["delegation-1"] == 0 || childEvents["delegation-2"] == 0 {
		t.Fatalf("persisted child events = %#v, want both delegations", childEvents)
	}
}

func assertDelegationToolSchema(t *testing.T, candidate tool.Tool) {
	t.Helper()
	function, ok := candidate.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		t.Fatalf("delegation tool %T does not expose a function declaration", candidate)
	}
	encoded, err := json.Marshal(function.Declaration().ParametersJsonSchema)
	if err != nil {
		t.Fatalf("marshal delegation schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatalf("decode delegation schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if schema["type"] != "object" || !ok {
		t.Fatalf("delegation schema = %#v", schema)
	}
	request, ok := properties["request"].(map[string]any)
	if !ok || request["type"] != "string" {
		t.Fatalf("delegation request schema = %#v", properties["request"])
	}
}

type parallelSubAgentModel struct {
	mu                sync.Mutex
	rootCalls         int
	activeChildren    int
	maxActiveChildren int
	followUpSeen      bool
	releaseChildren   chan struct{}
	releaseOnce       sync.Once
}

func newParallelSubAgentModel() *parallelSubAgentModel {
	return &parallelSubAgentModel{releaseChildren: make(chan struct{})}
}

func (m *parallelSubAgentModel) Name() string {
	return "parallel-subagent-test"
}

func (m *parallelSubAgentModel) GenerateContent(
	ctx context.Context,
	request *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if strings.Contains(systemInstructionText(request), "read-only specialist") {
			m.runChild(ctx, yield)
			return
		}

		m.mu.Lock()
		m.rootCalls++
		rootCall := m.rootCalls
		m.mu.Unlock()
		if rootCall == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{
						ID: "delegation-1", Name: "workspace_explorer",
						Args: map[string]any{"request": "Locate the affected code."},
					}},
					{FunctionCall: &genai.FunctionCall{
						ID: "delegation-2", Name: "code_reviewer",
						Args: map[string]any{"request": "Review the affected code."},
					}},
				},
			}}, nil)
			return
		}
		if err := validateFunctionCallResults(request.Contents); err != nil {
			yield(nil, err)
			return
		}
		if strings.Contains(contentsText(request.Contents), "Follow up on the review.") {
			m.mu.Lock()
			m.followUpSeen = true
			m.mu.Unlock()
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("Review complete.", genai.RoleModel),
		}, nil)
	}
}

func (m *parallelSubAgentModel) runChild(
	ctx context.Context,
	yield func(*model.LLMResponse, error) bool,
) {
	m.mu.Lock()
	m.activeChildren++
	if m.activeChildren > m.maxActiveChildren {
		m.maxActiveChildren = m.activeChildren
	}
	if m.activeChildren == 2 {
		m.releaseOnce.Do(func() { close(m.releaseChildren) })
	}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		yield(nil, ctx.Err())
		return
	case <-m.releaseChildren:
	}

	m.mu.Lock()
	m.activeChildren--
	m.mu.Unlock()
	yield(&model.LLMResponse{
		Content: genai.NewContentFromText("Specialist report.", genai.RoleModel),
	}, nil)
}

func (m *parallelSubAgentModel) maxConcurrentChildren() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxActiveChildren
}

func (m *parallelSubAgentModel) observedFollowUp() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.followUpSeen
}

func validateFunctionCallResults(contents []*genai.Content) error {
	for index, content := range contents {
		if content == nil || content.Role != genai.RoleModel {
			continue
		}
		expected := make(map[string]struct{})
		for _, part := range content.Parts {
			if part != nil && part.FunctionCall != nil {
				expected[part.FunctionCall.ID] = struct{}{}
			}
		}
		if len(expected) == 0 {
			continue
		}
		if index+1 >= len(contents) || contents[index+1] == nil ||
			contents[index+1].Role == genai.RoleModel {
			return fmt.Errorf("function calls at content %d are not followed by tool results", index)
		}
		for _, part := range contents[index+1].Parts {
			if part != nil && part.FunctionResponse != nil {
				delete(expected, part.FunctionResponse.ID)
			}
		}
		if len(expected) > 0 {
			return fmt.Errorf("function calls at content %d have missing tool results", index)
		}
	}
	return nil
}

func contentsText(contents []*genai.Content) string {
	var result strings.Builder
	for _, content := range contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil {
				result.WriteString(part.Text)
			}
		}
	}
	return result.String()
}

func systemInstructionText(request *model.LLMRequest) string {
	if request == nil || request.Config == nil || request.Config.SystemInstruction == nil {
		return ""
	}
	var result strings.Builder
	for _, part := range request.Config.SystemInstruction.Parts {
		if part != nil {
			result.WriteString(part.Text)
		}
	}
	return result.String()
}
