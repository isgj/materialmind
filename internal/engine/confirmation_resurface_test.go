package engine

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

func resurfaceTestEvent(callID string, confirmations map[string]toolconfirmation.ToolConfirmation) *session.Event {
	return &session.Event{
		InvocationID: "invocation-1",
		Author:       "workspace_agent",
		Branch:       "workspace_agent",
		LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID:       callID,
				Name:     "fetch_url",
				Response: map[string]any{"state": "approval_required"},
			}}},
		}},
		Actions: session.EventActions{
			RequestedToolConfirmations: confirmations,
			SkipSummarization:          true,
		},
	}
}

func resurfaceTestConfirmation(hint string) toolconfirmation.ToolConfirmation {
	return toolconfirmation.ToolConfirmation{
		Hint:    hint,
		Payload: map[string]any{"kind": "fetch_url", "url": "https://example.com/next"},
	}
}

func TestDanglingConfirmationRequestsReportsUnsurfacedRequests(t *testing.T) {
	requests := danglingConfirmationRequests([]*session.Event{
		resurfaceTestEvent("call-1", map[string]toolconfirmation.ToolConfirmation{
			"call-1": resurfaceTestConfirmation("Allow step 1?"),
		}),
	})
	if len(requests) != 1 {
		t.Fatalf("danglingConfirmationRequests() returned %d requests, want 1", len(requests))
	}
	request := requests[0]
	if request.callID != "call-1" || request.confirmation.Hint != "Allow step 1?" || request.source == nil {
		t.Fatalf("request = %+v", request)
	}
}

func TestDanglingConfirmationRequestsIgnoresSurfacedRequests(t *testing.T) {
	requests := danglingConfirmationRequests([]*session.Event{
		resurfaceTestEvent("fetch-1", map[string]toolconfirmation.ToolConfirmation{
			"fetch-1": resurfaceTestConfirmation("Allow?"),
		}),
		confirmationRequestEvent(),
	})
	if len(requests) != 0 {
		t.Fatalf("danglingConfirmationRequests() = %+v, want no requests", requests)
	}
}

func TestDanglingConfirmationRequestsSkipsPartialsAndPlainCalls(t *testing.T) {
	partial := resurfaceTestEvent("call-1", map[string]toolconfirmation.ToolConfirmation{
		"call-1": resurfaceTestConfirmation("Allow?"),
	})
	partial.Partial = true
	if requests := danglingConfirmationRequests([]*session.Event{partial}); len(requests) != 0 {
		t.Fatalf("partial event produced %d requests, want 0", len(requests))
	}
	plain := &session.Event{
		LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call-2", Name: "fetch_url"}}},
		}},
	}
	if requests := danglingConfirmationRequests([]*session.Event{plain}); len(requests) != 0 {
		t.Fatalf("plain model call produced %d requests, want 0", len(requests))
	}
}

func TestDanglingConfirmationRequestsKeepsFirstOccurrencePerCall(t *testing.T) {
	requests := danglingConfirmationRequests([]*session.Event{
		resurfaceTestEvent("call-1", map[string]toolconfirmation.ToolConfirmation{
			"call-1": resurfaceTestConfirmation("first"),
		}),
		resurfaceTestEvent("call-1", map[string]toolconfirmation.ToolConfirmation{
			"call-1": resurfaceTestConfirmation("second"),
		}),
	})
	if len(requests) != 1 || requests[0].confirmation.Hint != "first" {
		t.Fatalf("requests = %+v, want the single first request", requests)
	}
}

func TestDanglingConfirmationRequestsIsDeterministic(t *testing.T) {
	requests := danglingConfirmationRequests([]*session.Event{
		resurfaceTestEvent("call-b", map[string]toolconfirmation.ToolConfirmation{
			"call-b": resurfaceTestConfirmation("b"),
			"call-a": resurfaceTestConfirmation("a"),
		}),
	})
	if len(requests) != 2 || requests[0].callID != "call-a" || requests[1].callID != "call-b" {
		t.Fatalf("requests = %+v, want call-a then call-b", requests)
	}
}

func TestBuildResurfaceEventMirrorsADKConfirmationEvent(t *testing.T) {
	source := resurfaceTestEvent("call-1", nil)
	originals := []*genai.FunctionCall{
		{ID: "call-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com/one"}},
		{ID: "call-2", Name: "fetch_url", Args: map[string]any{"url": "https://example.com/two"}},
	}
	requests := []resurfaceConfirmation{
		{
			callID:       "call-1",
			confirmation: toolconfirmation.ToolConfirmation{Hint: "one", Payload: map[string]any{"kind": "fetch_url"}},
			originalCall: originals[0],
			source:       source,
		},
		{
			callID:       "call-2",
			confirmation: toolconfirmation.ToolConfirmation{Hint: "two", Payload: map[string]any{"kind": "fetch_url"}},
			originalCall: originals[1],
			source:       source,
		},
	}
	event := buildResurfaceEvent(context.Background(), "workspace_agent", requests)
	if event.Author != "workspace_agent" || event.Branch != "workspace_agent" ||
		event.InvocationID != "invocation-1" || event.ID == "" || event.Timestamp.IsZero() {
		t.Fatalf("event metadata = %+v", event)
	}
	if len(event.LongRunningToolIDs) != 2 ||
		event.LongRunningToolIDs[0] != "call-1" || event.LongRunningToolIDs[1] != "call-2" {
		t.Fatalf("LongRunningToolIDs = %v", event.LongRunningToolIDs)
	}
	if event.Content == nil || event.Content.Role != genai.RoleModel || len(event.Content.Parts) != 2 {
		t.Fatalf("event content = %+v", event.Content)
	}
	if event.Content.Parts[0].FunctionCall.ID == event.Content.Parts[1].FunctionCall.ID {
		t.Fatal("confirmation call IDs are not unique")
	}
	for index, part := range event.Content.Parts {
		call := part.FunctionCall
		if call == nil || call.Name != toolconfirmation.FunctionCallName || call.ID == "" {
			t.Fatalf("part %d = %+v", index, part)
		}
		original, err := toolconfirmation.OriginalCallFrom(call)
		if err != nil || original == nil || original.ID != originals[index].ID {
			t.Fatalf("OriginalCallFrom(part %d) = %v, %v", index, original, err)
		}
		confirmation, err := confirmationFromCall(call)
		if err != nil || confirmation.Hint != requests[index].confirmation.Hint {
			t.Fatalf("confirmationFromCall(part %d) = %v, %v", index, confirmation, err)
		}
	}
}

func TestBuildResurfaceEventFallsBackToAgentName(t *testing.T) {
	source := resurfaceTestEvent("call-1", nil)
	source.Author = ""
	requests := []resurfaceConfirmation{{
		callID:       "call-1",
		confirmation: resurfaceTestConfirmation("Allow?"),
		originalCall: &genai.FunctionCall{ID: "call-1", Name: "fetch_url"},
		source:       source,
	}}
	event := buildResurfaceEvent(context.Background(), "fallback_agent", requests)
	if event.Author != "fallback_agent" {
		t.Fatalf("event.Author = %q, want fallback_agent", event.Author)
	}
}

func TestResurfaceEventGroupsSeparatesScopes(t *testing.T) {
	parent := resurfaceTestEvent("call-1", nil)
	child := resurfaceTestEvent("call-2", nil)
	child.IsolationScope = "delegation-1"
	child.Branch = "workspace_agent.workspace_explorer"
	requests := []resurfaceConfirmation{
		{callID: "call-1", source: parent},
		{callID: "call-2", source: child},
		{callID: "call-3", source: child},
	}
	groups := resurfaceEventGroups(requests)
	if len(groups) != 2 {
		t.Fatalf("resurfaceEventGroups() returned %d groups, want 2", len(groups))
	}
	if len(groups[0]) != 1 || len(groups[1]) != 2 {
		t.Fatalf("group sizes = %d, %d, want 1, 2", len(groups[0]), len(groups[1]))
	}
}

func TestFindFunctionCallReturnsFirstMatch(t *testing.T) {
	original := &genai.FunctionCall{ID: "call-1", Name: "fetch_url"}
	events := []*session.Event{
		{LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "other", Name: "read_file"}}},
		}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: original}},
		}}},
	}
	if got := findFunctionCall(events, "call-1"); got != original {
		t.Fatalf("findFunctionCall() = %v, want the original call", got)
	}
	if got := findFunctionCall(events, "missing"); got != nil {
		t.Fatalf("findFunctionCall() = %v, want nil", got)
	}
}

func TestResurfaceResumedConfirmationsPersistsEventAndRegistersApproval(t *testing.T) {
	ctx := t.Context()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("open data store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(ctx, "OpenAI Responses", "openai-responses", "https://responses.example.test/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	modelRecord, err := dataStore.CreateLLMModel(ctx, provider.ID, "Model", "model-1", store.GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	engine := New(dataStore)
	sessionRecord, err := engine.CreateSession(ctx, workspace.ID, "Session", &modelRecord.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	runRecord, err := dataStore.CreateRun(ctx, sessionRecord.ID, modelRecord.ID, "message", store.RunGenerationOverrides{})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	seeded, err := engine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: sessionRecord.ID,
	})
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	modelCall := &genai.FunctionCall{ID: "call-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com/start"}}
	if err := engine.sessionService.AppendEvent(ctx, seeded.Session, &session.Event{
		InvocationID: "invocation-1",
		Author:       "workspace_agent",
		Branch:       "workspace_agent",
		LLMResponse:  model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: modelCall}}}},
	}); err != nil {
		t.Fatalf("append model call: %v", err)
	}
	resumeEvent := resurfaceTestEvent("call-1", map[string]toolconfirmation.ToolConfirmation{
		"call-1": {
			Hint:    "Allow the agent to fetch https://example.com/redirected?",
			Payload: map[string]any{"kind": "fetch_url", "url": "https://example.com/redirected"},
		},
	})
	if err := engine.sessionService.AppendEvent(ctx, seeded.Session, resumeEvent); err != nil {
		t.Fatalf("append resume event: %v", err)
	}

	engine.hub.Create(runRecord.ID)
	engine.active[sessionRecord.ID] = &activeRun{
		runID:                  runRecord.ID,
		pendingApprovals:       make(map[string]*pendingToolApproval),
		pendingUserInputs:      make(map[string]*pendingUserInput),
		pendingMCPElicitations: make(map[string]*pendingMCPElicitation),
	}

	pendingApprovals := make(map[string]ToolApprovalRequest)
	if err := engine.resurfaceResumedConfirmations(ctx, runRecord, "workspace_agent", []*session.Event{resumeEvent}, pendingApprovals); err != nil {
		t.Fatalf("resurfaceResumedConfirmations() error = %v", err)
	}
	if len(pendingApprovals) != 1 {
		t.Fatalf("pendingApprovals has %d entries, want 1", len(pendingApprovals))
	}
	var request ToolApprovalRequest
	for _, entry := range pendingApprovals {
		request = entry
	}
	if request.ToolCallID != "call-1" || request.ToolName != "fetch_url" ||
		request.Payload["url"] != "https://example.com/redirected" {
		t.Fatalf("request = %+v", request)
	}

	verified, err := engine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: sessionRecord.ID,
	})
	if err != nil {
		t.Fatalf("get verified session: %v", err)
	}
	var resurfaced *session.Event
	for event := range verified.Session.Events().All() {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil && isConfirmationCall(part.FunctionCall.Name) {
				resurfaced = event
			}
		}
	}
	if resurfaced == nil {
		t.Fatal("no resurfaced confirmation event in the session")
	}
	if len(resurfaced.LongRunningToolIDs) != 1 || resurfaced.LongRunningToolIDs[0] != "call-1" {
		t.Fatalf("LongRunningToolIDs = %v, want [call-1]", resurfaced.LongRunningToolIDs)
	}

	decision, err := engine.ResolveToolApproval(ctx, runRecord.ID, request.ID, true, "", "")
	if err != nil {
		t.Fatalf("ResolveToolApproval() error = %v", err)
	}
	if !decision.Approved || decision.ToolCallID != "call-1" {
		t.Fatalf("decision = %+v", decision)
	}
	engine.hub.Complete(runRecord.ID)
	subscribed, ok := engine.hub.Subscribe(ctx, runRecord.ID, 0)
	if !ok {
		t.Fatal("hub.Subscribe() ok = false")
	}
	approvalPublished := false
	for hubEvent := range subscribed {
		if hubEvent.Type == "tool_approval" {
			approvalPublished = true
		}
	}
	if !approvalPublished {
		t.Fatal("the run hub did not publish the resurfaced tool approval")
	}
}

func TestResurfaceEventDrivesADKResume(t *testing.T) {
	ctx := t.Context()
	fakeModel := &resurfaceTestModel{}
	resumeTool, err := functiontool.New(functiontool.Config{
		Name:        "resume_tool",
		Description: "Requests a confirmation, and requests a second one when resumed.",
	}, func(toolCtx agent.Context, args resurfaceTestArgs) (resurfaceTestResult, error) {
		confirmation := toolCtx.ToolConfirmation()
		if confirmation == nil {
			if err := toolCtx.RequestConfirmation("Allow step 1?", map[string]any{"kind": "resume_test", "step": 1}); err != nil {
				return resurfaceTestResult{}, err
			}
			toolCtx.Actions().SkipSummarization = true
			return resurfaceTestResult{State: "approval_required", Step: 1}, nil
		}
		if !confirmation.Confirmed {
			return resurfaceTestResult{State: "denied"}, nil
		}
		step := 1
		if payload, ok := confirmation.Payload.(map[string]any); ok {
			if value, ok := payload["step"].(float64); ok {
				step = int(value)
			}
		}
		if step >= 2 {
			return resurfaceTestResult{State: "done", Step: step}, nil
		}
		if err := toolCtx.RequestConfirmation(fmt.Sprintf("Allow step %d?", step+1), map[string]any{"kind": "resume_test", "step": step + 1}); err != nil {
			return resurfaceTestResult{}, err
		}
		toolCtx.Actions().SkipSummarization = true
		return resurfaceTestResult{State: "approval_required", Step: step + 1}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  "resurface_agent",
		Model: fakeModel,
		Tools: []tool.Tool{resumeTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	sessionService := session.InMemoryService()
	created, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "resurface-test", UserID: "resurface-user", SessionID: "resurface-session",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	sessionID := created.Session.ID()
	agentRunner, err := runner.New(runner.Config{
		AppName: "resurface-test", Agent: agentInstance, SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	var iterationEvents []*session.Event
	var firstConfirmation *genai.FunctionCall
	for event, runErr := range agentRunner.Run(
		ctx, "resurface-user", sessionID, genai.NewContentFromText("Run the tool", genai.RoleUser), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("first runner.Run() error = %v", runErr)
		}
		if event == nil {
			continue
		}
		iterationEvents = append(iterationEvents, event)
		if confirmation := resurfaceConfirmationPart(event); confirmation != nil {
			firstConfirmation = confirmation
		}
	}
	if firstConfirmation == nil {
		t.Fatal("first runner.Run() emitted no confirmation call")
	}
	if fakeModel.calls != 1 {
		t.Fatalf("model calls after the first confirmation = %d, want 1", fakeModel.calls)
	}

	iterationEvents = nil
	for event, runErr := range agentRunner.Run(
		ctx, "resurface-user", sessionID, resurfaceConfirmationDecision(firstConfirmation.ID, 1), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("resume runner.Run() error = %v", runErr)
		}
		if event == nil {
			continue
		}
		iterationEvents = append(iterationEvents, event)
		if resurfaceConfirmationPart(event) != nil {
			t.Fatal("resume runner.Run() surfaced a confirmation call; the resurface path is not needed")
		}
	}
	dangling := danglingConfirmationRequests(iterationEvents)
	if len(dangling) != 1 || dangling[0].callID != "resume-call-1" {
		t.Fatalf("danglingConfirmationRequests() = %+v, want the step-2 request for resume-call-1", dangling)
	}

	response, err := sessionService.Get(ctx, &session.GetRequest{
		AppName: "resurface-test", UserID: "resurface-user", SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("sessionService.Get() error = %v", err)
	}
	var sessionEvents []*session.Event
	for event := range response.Session.Events().All() {
		sessionEvents = append(sessionEvents, event)
	}
	dangling[0].originalCall = findFunctionCall(sessionEvents, dangling[0].callID)
	if dangling[0].originalCall == nil {
		t.Fatal("the original resume_tool call is missing from the session")
	}
	synthetic := buildResurfaceEvent(ctx, "resurface_agent", dangling)
	if err := sessionService.AppendEvent(ctx, response.Session, synthetic); err != nil {
		t.Fatalf("sessionService.AppendEvent() error = %v", err)
	}
	resurfacedCall := resurfaceConfirmationPart(synthetic)
	if resurfacedCall == nil {
		t.Fatal("the synthetic event carries no confirmation call")
	}

	var done map[string]any
	for event, runErr := range agentRunner.Run(
		ctx, "resurface-user", sessionID, resurfaceConfirmationDecision(resurfacedCall.ID, 2), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("second resume runner.Run() error = %v", runErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Name == "resume_tool" {
				done = part.FunctionResponse.Response
			}
		}
	}
	if done == nil || done["state"] != "done" {
		t.Fatalf("resumed tool result = %v, want done", done)
	}
	// Turn 1 and turn 3 each use one model call. ADK's resume path also runs a
	// model step after the tool requests the next confirmation, even though the
	// tool set SkipSummarization; that step is what made the buggy run end
	// "completed" while the approval request stayed unsurfaced.
	if fakeModel.calls != 3 {
		t.Fatalf("model calls = %d, want 3", fakeModel.calls)
	}
}

func resurfaceConfirmationPart(event *session.Event) *genai.FunctionCall {
	if event == nil || event.Content == nil {
		return nil
	}
	for _, part := range event.Content.Parts {
		if part != nil && part.FunctionCall != nil && isConfirmationCall(part.FunctionCall.Name) {
			return part.FunctionCall
		}
	}
	return nil
}

func resurfaceConfirmationDecision(approvalID string, step int) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:   approvalID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": true,
				"payload":   map[string]any{"kind": "resume_test", "step": step},
			},
		},
	}}}
}

type resurfaceTestArgs struct {
	Token string `json:"token"`
}

type resurfaceTestResult struct {
	State string `json:"state"`
	Step  int    `json:"step"`
}

type resurfaceTestModel struct {
	calls int
}

func (m *resurfaceTestModel) Name() string { return "resurface-test-model" }

func (m *resurfaceTestModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   "resume-call-1",
					Name: "resume_tool",
					Args: map[string]any{"token": "t-1"},
				}}},
			}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("The tool completed.", genai.RoleModel)}, nil)
	}
}

// resurfaceTestEngine opens a store-backed engine with a session and a run,
// ready for resurface tests.
func resurfaceTestEngine(t *testing.T) (*Engine, store.Run) {
	t.Helper()
	ctx := t.Context()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("open data store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(ctx, "OpenAI Responses", "openai-responses", "https://responses.example.test/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	modelRecord, err := dataStore.CreateLLMModel(ctx, provider.ID, "Model", "model-1", store.GenerationSettings{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	engine := New(dataStore)
	sessionRecord, err := engine.CreateSession(ctx, workspace.ID, "Session", &modelRecord.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	runRecord, err := dataStore.CreateRun(ctx, sessionRecord.ID, modelRecord.ID, "message", store.RunGenerationOverrides{})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return engine, runRecord
}

func resurfaceActivateRun(t *testing.T, engine *Engine, runRecord store.Run) {
	t.Helper()
	engine.hub.Create(runRecord.ID)
	engine.active[runRecord.SessionID] = &activeRun{
		runID:                  runRecord.ID,
		pendingApprovals:       make(map[string]*pendingToolApproval),
		pendingUserInputs:      make(map[string]*pendingUserInput),
		pendingMCPElicitations: make(map[string]*pendingMCPElicitation),
	}
}

func resurfaceSubAgentResumeTool(t *testing.T) tool.Tool {
	t.Helper()
	resumeTool, err := functiontool.New(functiontool.Config{
		Name:        "resume_tool",
		Description: "Requests a confirmation, and requests a second one when resumed.",
	}, func(toolCtx agent.Context, args resurfaceTestArgs) (resurfaceTestResult, error) {
		confirmation := toolCtx.ToolConfirmation()
		if confirmation == nil {
			if err := toolCtx.RequestConfirmation("Allow step 1?", map[string]any{"kind": "resume_test", "step": 1}); err != nil {
				return resurfaceTestResult{}, err
			}
			toolCtx.Actions().SkipSummarization = true
			return resurfaceTestResult{State: "approval_required", Step: 1}, nil
		}
		if !confirmation.Confirmed {
			return resurfaceTestResult{State: "denied"}, nil
		}
		step := 1
		if payload, ok := confirmation.Payload.(map[string]any); ok {
			if value, ok := payload["step"].(float64); ok {
				step = int(value)
			}
		}
		if step >= 2 {
			return resurfaceTestResult{State: "done", Step: step}, nil
		}
		if err := toolCtx.RequestConfirmation(fmt.Sprintf("Allow step %d?", step+1), map[string]any{"kind": "resume_test", "step": step + 1}); err != nil {
			return resurfaceTestResult{}, err
		}
		toolCtx.Actions().SkipSummarization = true
		return resurfaceTestResult{State: "approval_required", Step: step + 1}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	return resumeTool
}

func TestResurfaceSubAgentConfirmationsPersistsDelegatedEventAndRegistersApproval(t *testing.T) {
	ctx := t.Context()
	engine, runRecord := resurfaceTestEngine(t)
	profile, ok := subAgentProfileForName("code_reviewer")
	if !ok {
		t.Fatal("code_reviewer profile is missing")
	}
	childSessions := session.InMemoryService()
	childCreated, err := childSessions.Create(ctx, &session.CreateRequest{
		AppName: subAgentAppName, UserID: UserID, SessionID: "child-session-1",
	})
	if err != nil {
		t.Fatalf("childSessions.Create() error = %v", err)
	}
	childSession := childCreated.Session
	if err := childSessions.AppendEvent(ctx, childSession, &session.Event{
		InvocationID: "child-invocation-1",
		Author:       profile.Name,
		Branch:       profile.Name,
		LLMResponse: model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "read_file", Args: map[string]any{"path": "README.md"}},
		}}}},
	}); err != nil {
		t.Fatalf("seed child call: %v", err)
	}
	resumeEvent := &session.Event{
		InvocationID: "child-invocation-1",
		Author:       profile.Name,
		Branch:       profile.Name,
		LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID:       "call-1",
				Name:     "read_file",
				Response: map[string]any{"state": "approval_required"},
			}}},
		}},
		Actions: session.EventActions{
			RequestedToolConfirmations: map[string]toolconfirmation.ToolConfirmation{
				"call-1": {
					Hint:    "Allow the agent to read /etc/passwd?",
					Payload: map[string]any{"kind": "read_file", "path": "/etc/passwd"},
				},
			},
			SkipSummarization: true,
		},
	}
	if err := childSessions.AppendEvent(ctx, childSession, resumeEvent); err != nil {
		t.Fatalf("seed child resume event: %v", err)
	}

	resurfaceActivateRun(t, engine, runRecord)
	pendingApprovals := make(map[string]ToolApprovalRequest)
	if err := engine.resurfaceSubAgentConfirmations(
		ctx, runRecord, profile, childSessions, childSession,
		"parent-invocation-1", "delegation-1", []*session.Event{resumeEvent}, pendingApprovals,
	); err != nil {
		t.Fatalf("resurfaceSubAgentConfirmations() error = %v", err)
	}
	if len(pendingApprovals) != 1 {
		t.Fatalf("pendingApprovals has %d entries, want 1", len(pendingApprovals))
	}
	var request ToolApprovalRequest
	for _, entry := range pendingApprovals {
		request = entry
	}
	if request.ToolCallID != "call-1" || request.ToolName != "read_file" || request.InvocationID != "child-invocation-1" {
		t.Fatalf("request = %+v", request)
	}

	childVerified, err := childSessions.Get(ctx, &session.GetRequest{
		AppName: subAgentAppName, UserID: UserID, SessionID: childSession.ID(),
	})
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	var resurfaced *session.Event
	for event := range childVerified.Session.Events().All() {
		if resurfaceConfirmationPart(event) != nil {
			resurfaced = event
		}
	}
	if resurfaced == nil {
		t.Fatal("no resurfaced confirmation event in the child session")
	}
	if resurfaced.InvocationID != "child-invocation-1" {
		t.Fatalf("resurfaced event InvocationID = %q, want the child invocation", resurfaced.InvocationID)
	}
	if len(resurfaced.LongRunningToolIDs) != 1 || resurfaced.LongRunningToolIDs[0] != "call-1" {
		t.Fatalf("LongRunningToolIDs = %v, want [call-1]", resurfaced.LongRunningToolIDs)
	}

	verified, err := engine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: runRecord.SessionID,
	})
	if err != nil {
		t.Fatalf("get parent session: %v", err)
	}
	var delegated *session.Event
	for event := range verified.Session.Events().All() {
		if resurfaceConfirmationPart(event) != nil {
			delegated = event
		}
	}
	if delegated == nil {
		t.Fatal("no delegated resurfaced event in the parent transcript")
	}
	if delegated.InvocationID != "parent-invocation-1" || delegated.Author != profile.Name ||
		delegated.Branch != "workspace_agent."+profile.Name || delegated.IsolationScope != "delegation-1" {
		t.Fatalf("delegated event metadata = %+v", delegated)
	}

	decision, err := engine.ResolveToolApproval(ctx, runRecord.ID, request.ID, true, "", "")
	if err != nil {
		t.Fatalf("ResolveToolApproval() error = %v", err)
	}
	if !decision.Approved || decision.ToolCallID != "call-1" {
		t.Fatalf("decision = %+v", decision)
	}

	engine.hub.Complete(runRecord.ID)
	subscribed, ok := engine.hub.Subscribe(ctx, runRecord.ID, 0)
	if !ok {
		t.Fatal("hub.Subscribe() ok = false")
	}
	approvalPublished := false
	for hubEvent := range subscribed {
		if hubEvent.Type == "tool_approval" {
			approvalPublished = true
		}
	}
	if !approvalPublished {
		t.Fatal("the run hub did not publish the resurfaced sub-agent tool approval")
	}
}

func TestResurfaceSubAgentConfirmationsDrivesChildResume(t *testing.T) {
	ctx := t.Context()
	engine, runRecord := resurfaceTestEngine(t)
	profile, ok := subAgentProfileForName("code_reviewer")
	if !ok {
		t.Fatal("code_reviewer profile is missing")
	}
	fakeModel := &resurfaceTestModel{}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:  profile.Name,
		Model: fakeModel,
		Tools: []tool.Tool{resurfaceSubAgentResumeTool(t)},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	childSessions := session.InMemoryService()
	childCreated, err := childSessions.Create(ctx, &session.CreateRequest{
		AppName: subAgentAppName, UserID: UserID, SessionID: "child-session-2",
	})
	if err != nil {
		t.Fatalf("childSessions.Create() error = %v", err)
	}
	childRunner, err := runner.New(runner.Config{
		AppName: subAgentAppName, Agent: agentInstance, SessionService: childSessions,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	childSessionID := childCreated.Session.ID()

	var iterationEvents []*session.Event
	var firstConfirmation *genai.FunctionCall
	for event, runErr := range childRunner.Run(
		ctx, UserID, childSessionID, genai.NewContentFromText("Run the tool", genai.RoleUser), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("first child runner.Run() error = %v", runErr)
		}
		if event == nil {
			continue
		}
		iterationEvents = append(iterationEvents, event)
		if confirmation := resurfaceConfirmationPart(event); confirmation != nil {
			firstConfirmation = confirmation
		}
	}
	if firstConfirmation == nil {
		t.Fatal("first child runner.Run() emitted no confirmation call")
	}
	if fakeModel.calls != 1 {
		t.Fatalf("model calls after the first confirmation = %d, want 1", fakeModel.calls)
	}

	iterationEvents = nil
	for event, runErr := range childRunner.Run(
		ctx, UserID, childSessionID, resurfaceConfirmationDecision(firstConfirmation.ID, 1), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("resume child runner.Run() error = %v", runErr)
		}
		if event == nil {
			continue
		}
		iterationEvents = append(iterationEvents, event)
		if resurfaceConfirmationPart(event) != nil {
			t.Fatal("resume child runner.Run() surfaced a confirmation call; the resurface path is not needed")
		}
	}

	resurfaceActivateRun(t, engine, runRecord)
	pendingApprovals := make(map[string]ToolApprovalRequest)
	if err := engine.resurfaceSubAgentConfirmations(
		ctx, runRecord, profile, childSessions, childCreated.Session,
		"parent-invocation-1", "delegation-1", iterationEvents, pendingApprovals,
	); err != nil {
		t.Fatalf("resurfaceSubAgentConfirmations() error = %v", err)
	}
	if len(pendingApprovals) != 1 {
		t.Fatalf("pendingApprovals has %d entries, want 1", len(pendingApprovals))
	}
	var request ToolApprovalRequest
	for _, entry := range pendingApprovals {
		request = entry
	}
	if request.ToolCallID != "resume-call-1" {
		t.Fatalf("request.ToolCallID = %q, want resume-call-1", request.ToolCallID)
	}

	var done map[string]any
	for event, runErr := range childRunner.Run(
		ctx, UserID, childSessionID, resurfaceConfirmationDecision(request.ID, 2), agent.RunConfig{},
	) {
		if runErr != nil {
			t.Fatalf("second resume child runner.Run() error = %v", runErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Name == "resume_tool" {
				done = part.FunctionResponse.Response
			}
		}
	}
	if done == nil || done["state"] != "done" {
		t.Fatalf("resumed child tool result = %v, want done", done)
	}
	if fakeModel.calls != 3 {
		t.Fatalf("model calls = %d, want 3", fakeModel.calls)
	}
}
