package engine

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"
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
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/agentskills"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
	"materialmind/internal/workspacetools"
)

const engineCommandHelperEnvironment = "MATERIALMIND_ENGINE_COMMAND_HELPER"

type concurrentCommandApprovalModel struct {
	command     string
	firstFiles  [2]string
	secondFiles [2]string
	calls       int
	sawResults  bool
	sawOrdinary bool
}

func (*concurrentCommandApprovalModel) Name() string {
	return "concurrent-command-approval-model"
}

func (m *concurrentCommandApprovalModel) GenerateContent(
	_ context.Context,
	request *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		switch m.calls {
		case 1:
			yield(&model.LLMResponse{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{
						ID: "ordinary-fast", Name: "ordinary_tool",
					}},
					{FunctionCall: m.commandCall("command-first", "first", m.firstFiles)},
					{FunctionCall: m.commandCall("command-second", "second", m.secondFiles)},
				},
			}}, nil)
		case 2:
			responses := make(map[string]bool, 3)
			for _, content := range request.Contents {
				if content == nil {
					continue
				}
				for _, part := range content.Parts {
					if part != nil && part.FunctionResponse != nil {
						responses[part.FunctionResponse.ID] = true
					}
				}
			}
			m.sawResults = responses["command-first"] && responses["command-second"]
			m.sawOrdinary = responses["ordinary-fast"]
			if !m.sawResults || !m.sawOrdinary {
				yield(nil, fmt.Errorf("function responses = %#v, want ordinary and both commands", responses))
				return
			}
			yield(&model.LLMResponse{
				Content: genai.NewContentFromText("Both commands completed.", genai.RoleModel),
			}, nil)
		default:
			yield(nil, fmt.Errorf("unexpected model call %d", m.calls))
		}
	}
}

func (m *concurrentCommandApprovalModel) commandCall(
	id, name string,
	files [2]string,
) *genai.FunctionCall {
	return &genai.FunctionCall{
		ID:   id,
		Name: toolpolicy.ToolRunCommand,
		Args: map[string]any{
			"command": m.command,
			"args": []string{
				"-test.v",
				"-test.run=^TestEngineRunCommandHelper$",
				"--",
				"wait-file",
				name,
				files[0],
				files[1],
			},
			"timeoutSeconds": 10,
		},
	}
}

func TestEngineApprovedCommandsStartAndFinishIndependently(t *testing.T) {
	t.Setenv(engineCommandHelperEnvironment, "1")
	workspaceRoot := t.TempDir()
	dataStore, err := store.Open(t.Context(), filepath.Join(workspaceRoot, "data", "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(t.Context(), "Workspace", workspaceRoot)
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(
		t.Context(), "Provider", "anthropic", "https://api.anthropic.com", "",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	modelRecord, err := dataStore.CreateLLMModel(
		t.Context(),
		provider.ID,
		"Model",
		"test-model",
		store.GenerationSettings{ContextWindowTokens: 8192, MaxOutputTokens: 1024},
	)
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}

	runEngine := New(dataStore)
	sessionRecord, err := runEngine.CreateSession(
		t.Context(), workspace.ID, "Concurrent commands", &modelRecord.ID,
	)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	runRecord, err := dataStore.CreateRun(
		t.Context(), sessionRecord.ID, modelRecord.ID, "Run both commands",
		store.RunGenerationOverrides{},
	)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	runContext, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	firstFiles := [2]string{
		filepath.Join(workspaceRoot, "release-first"),
		filepath.Join(workspaceRoot, "heartbeat-first"),
	}
	secondFiles := [2]string{
		filepath.Join(workspaceRoot, "release-second"),
		filepath.Join(workspaceRoot, "heartbeat-second"),
	}
	runEngine.hub.Create(runRecord.ID)
	runEngine.mu.Lock()
	runEngine.active[sessionRecord.ID] = &activeRun{
		runID:                   runRecord.ID,
		pendingApprovals:        make(map[string]*pendingToolApproval),
		publishedCommandResults: make(map[string]struct{}),
		pendingUserInputs:       make(map[string]*pendingUserInput),
	}
	runEngine.mu.Unlock()

	var runnerDone chan struct{}
	t.Cleanup(func() {
		for _, release := range []string{firstFiles[0], secondFiles[0]} {
			_ = os.WriteFile(release, []byte("release"), 0o600)
		}
		cancel()
		if runnerDone != nil {
			select {
			case <-runnerDone:
			case <-time.After(2 * time.Second):
			}
		}
		runEngine.mu.Lock()
		delete(runEngine.active, sessionRecord.ID)
		runEngine.mu.Unlock()
		runEngine.hub.Complete(runRecord.ID)
	})

	workspaceOptions := runEngine.workspaceToolOptions(runRecord)
	var approvedCalls sync.Map
	requestApproval := workspaceOptions.RequestApproval
	workspaceOptions.RequestApproval = func(
		toolContext context.Context,
		request workspacetools.ToolApprovalRequest,
	) (workspacetools.ToolApprovalDecision, error) {
		decision, requestErr := requestApproval(toolContext, request)
		if requestErr == nil && decision.Approved {
			approvedCalls.Store(request.ToolCallID, true)
		}
		return decision, requestErr
	}
	preapprovalOutput := make(chan string, 1)
	publishOutput := workspaceOptions.CommandOutput
	workspaceOptions.CommandOutput = func(event workspacetools.CommandOutputEvent) {
		if _, approved := approvedCalls.Load(event.ToolCallID); !approved {
			select {
			case preapprovalOutput <- event.ToolCallID:
			default:
			}
		}
		publishOutput(event)
	}
	workspaceTools, err := workspacetools.New(
		workspaceRoot,
		toolpolicy.DefaultPermissions(),
		agentskills.Catalog{},
		workspaceOptions,
	)
	if err != nil {
		t.Fatalf("workspacetools.New() error = %v", err)
	}
	commandTools := filterTools(workspaceTools, []string{toolpolicy.ToolRunCommand})
	if len(commandTools) != 1 {
		t.Fatalf("run_command tool count = %d, want 1", len(commandTools))
	}
	ordinaryCompleted := make(chan struct{})
	ordinaryTool, err := functiontool.New(
		functiontool.Config{
			Name: "ordinary_tool", Description: "returns immediately",
		},
		func(agent.Context, struct{}) (map[string]any, error) {
			close(ordinaryCompleted)
			return map[string]any{"state": "completed"}, nil
		},
	)
	if err != nil {
		t.Fatalf("create ordinary tool: %v", err)
	}

	scriptedModel := &concurrentCommandApprovalModel{
		command: os.Args[0], firstFiles: firstFiles, secondFiles: secondFiles,
	}
	coordinatedModel := &approvalYieldModel{LLM: &mixedToolBatchModel{LLM: scriptedModel}}
	agentInstance, err := llmagent.New(llmagent.Config{
		Name: "workspace_agent", Model: coordinatedModel,
		Tools:               []tool.Tool{ordinaryTool, commandTools[0]},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{rejectMalformedFunctionArguments},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	agentRunner, err := runner.New(runner.Config{
		AppName: AppName, Agent: agentInstance,
		SessionService: runEngine.sessionService.RunnerService(),
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	events, ok := runEngine.hub.Subscribe(runContext, runRecord.ID, 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}

	runnerDone = make(chan struct{})
	var runnerErr error
	go func() {
		defer close(runnerDone)
		for event, runErr := range agentRunner.Run(
			withApprovalYield(runContext, false),
			UserID,
			runRecord.SessionID,
			genai.NewContentFromText(runRecord.UserMessage, genai.RoleUser),
			agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
		) {
			if runErr != nil {
				runnerErr = runErr
				return
			}
			if event != nil {
				runEngine.publishEvent(runRecord, event)
			}
		}
	}()

	approvalIDs := make(map[string]string, 2)
	for len(approvalIDs) < 2 {
		select {
		case event := <-events:
			if event.Type != "tool_approval" {
				continue
			}
			request, requestOK := event.Data.(ToolApprovalRequest)
			if !requestOK {
				t.Fatalf("tool approval event data = %T", event.Data)
			}
			approvalIDs[request.ToolCallID] = request.ID
		case <-runContext.Done():
			t.Fatalf("timed out waiting for tool approvals: %v", runContext.Err())
		}
	}
	select {
	case <-ordinaryCompleted:
	case <-runContext.Done():
		t.Fatalf("ordinary sibling remained blocked by command approvals: %v", runContext.Err())
	}

	approveEngineCommand(t, runContext, runEngine, runRecord.ID, approvalIDs["command-first"])
	waitForEngineCommandOutput(t, runContext, events, "command-first", "started:first")
	waitForFile(t, runContext, firstFiles[1])
	assertNoEngineCommandOutput(t, runEngine, runRecord.ID, "command-second")

	approveEngineCommand(t, runContext, runEngine, runRecord.ID, approvalIDs["command-second"])
	waitForEngineCommandOutput(t, runContext, events, "command-second", "started:second")
	assertFileKeepsChanging(t, runContext, firstFiles[1])

	if err := os.WriteFile(firstFiles[0], []byte("release"), 0o600); err != nil {
		t.Fatalf("release first command: %v", err)
	}
	waitForEngineCommandResult(t, runContext, events, "command-first")
	assertNoEngineCommandResult(t, runEngine, runRecord.ID, "command-second")
	assertFileKeepsChanging(t, runContext, secondFiles[1])
	select {
	case <-runnerDone:
		t.Fatal("runner finished while the second command was still blocked")
	default:
	}
	if err := os.WriteFile(secondFiles[0], []byte("release"), 0o600); err != nil {
		t.Fatalf("release second command: %v", err)
	}
	select {
	case <-runnerDone:
		if runnerErr != nil {
			t.Fatalf("runner.Run() error = %v", runnerErr)
		}
	case <-runContext.Done():
		t.Fatalf("runner did not finish: %v", runContext.Err())
	}
	if scriptedModel.calls != 2 || !scriptedModel.sawResults || !scriptedModel.sawOrdinary {
		t.Fatalf(
			"model calls = %d, saw commands = %t, saw ordinary = %t",
			scriptedModel.calls,
			scriptedModel.sawResults,
			scriptedModel.sawOrdinary,
		)
	}
	if runEngine.WaitingForUser(sessionRecord.ID) {
		t.Fatal("WaitingForUser() = true after commands completed")
	}
	select {
	case callID := <-preapprovalOutput:
		t.Fatalf("command %s produced output before its approval handler returned", callID)
	default:
	}
	assertEngineApprovalEventOrder(t, runEngine, runRecord.ID, approvalIDs)
	assertSingleEngineCommandResults(t, runEngine, runRecord.ID, "command-first", "command-second")
	assertSingleEngineToolResult(t, runEngine, runRecord.ID, "ordinary-fast", "ordinary_tool")
	assertPersistedCommandResponses(
		t,
		runContext,
		runEngine,
		runRecord.SessionID,
		"command-first",
		"command-second",
	)
}

func assertPersistedCommandResponses(
	t *testing.T,
	ctx context.Context,
	runEngine *Engine,
	sessionID string,
	toolCallIDs ...string,
) {
	t.Helper()
	loaded, err := runEngine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("load persisted ADK session: %v", err)
	}
	responseCounts := make(map[string]int, len(toolCallIDs))
	for _, toolCallID := range toolCallIDs {
		responseCounts[toolCallID] = 0
	}
	for event := range loaded.Session.Events().All() {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.Name == toolconfirmation.FunctionCallName {
				t.Fatalf("custom command approval persisted a native confirmation call: %#v", part.FunctionCall)
			}
			if part.FunctionResponse == nil {
				continue
			}
			if part.FunctionResponse.Name == toolconfirmation.FunctionCallName {
				t.Fatalf("custom command approval persisted a native confirmation response: %#v", part.FunctionResponse)
			}
			if part.FunctionResponse.Name != toolpolicy.ToolRunCommand {
				continue
			}
			if _, expected := responseCounts[part.FunctionResponse.ID]; !expected {
				continue
			}
			if part.FunctionResponse.Response["state"] == "approval_required" {
				t.Fatalf("persisted command approval placeholder: %#v", part.FunctionResponse)
			}
			responseCounts[part.FunctionResponse.ID]++
		}
	}
	for toolCallID, count := range responseCounts {
		if count != 1 {
			t.Fatalf("persisted %s response count = %d, want 1", toolCallID, count)
		}
	}
}

func approveEngineCommand(
	t *testing.T,
	ctx context.Context,
	runEngine *Engine,
	runID, approvalID string,
) {
	t.Helper()
	if _, err := runEngine.ResolveToolApproval(ctx, runID, approvalID, true, "", ""); err != nil {
		t.Fatalf("ResolveToolApproval() error = %v", err)
	}
}

func waitForEngineCommandOutput(
	t *testing.T,
	ctx context.Context,
	events <-chan StreamEvent,
	toolCallID, text string,
) {
	t.Helper()
	var combined strings.Builder
	for {
		select {
		case event := <-events:
			if event.Type != "command_output" {
				continue
			}
			output, ok := event.Data.(workspacetools.CommandOutputEvent)
			if !ok || output.ToolCallID != toolCallID {
				continue
			}
			combined.WriteString(output.Text)
			if strings.Contains(combined.String(), text) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s output %q: %v", toolCallID, text, ctx.Err())
		}
	}
}

func assertNoEngineCommandOutput(
	t *testing.T,
	runEngine *Engine,
	runID, toolCallID string,
) {
	t.Helper()
	for _, event := range engineHubEvents(t, runEngine, runID) {
		output, ok := event.Data.(workspacetools.CommandOutputEvent)
		if event.Type == "command_output" && ok && output.ToolCallID == toolCallID {
			t.Fatalf("%s emitted output before its approval: %#v", toolCallID, output)
		}
	}
}

func waitForEngineCommandResult(
	t *testing.T,
	ctx context.Context,
	events <-chan StreamEvent,
	toolCallID string,
) {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed before %s completed", toolCallID)
			}
			currentID, output, result := engineCommandResult(event)
			if !result || currentID != toolCallID {
				continue
			}
			if output["state"] != "completed" {
				t.Fatalf("%s result = %#v, want completed", toolCallID, output)
			}
			return
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s result: %v", toolCallID, ctx.Err())
		}
	}
}

func assertNoEngineCommandResult(
	t *testing.T,
	runEngine *Engine,
	runID, toolCallID string,
) {
	t.Helper()
	for _, event := range engineHubEvents(t, runEngine, runID) {
		currentID, output, result := engineCommandResult(event)
		if result && currentID == toolCallID {
			t.Fatalf("%s completed while still running: %#v", toolCallID, output)
		}
	}
}

func assertSingleEngineCommandResults(
	t *testing.T,
	runEngine *Engine,
	runID string,
	toolCallIDs ...string,
) {
	t.Helper()
	counts := make(map[string]int, len(toolCallIDs))
	for _, toolCallID := range toolCallIDs {
		counts[toolCallID] = 0
	}
	for _, event := range engineHubEvents(t, runEngine, runID) {
		toolCallID, output, result := engineCommandResult(event)
		if !result {
			continue
		}
		if _, expected := counts[toolCallID]; !expected {
			continue
		}
		if output["state"] != "completed" {
			t.Fatalf("%s result = %#v, want completed", toolCallID, output)
		}
		counts[toolCallID]++
	}
	for toolCallID, count := range counts {
		if count != 1 {
			t.Fatalf("%s tool_result count = %d, want 1", toolCallID, count)
		}
	}
}

func assertSingleEngineToolResult(
	t *testing.T,
	runEngine *Engine,
	runID, toolCallID, toolName string,
) {
	t.Helper()
	count := 0
	for _, event := range engineHubEvents(t, runEngine, runID) {
		if event.Type != "tool_result" {
			continue
		}
		payload, ok := event.Data.(map[string]any)
		if ok && payload["id"] == toolCallID && payload["name"] == toolName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%s tool_result count = %d, want 1", toolCallID, count)
	}
}

func engineCommandResult(event StreamEvent) (string, map[string]any, bool) {
	if event.Type != "tool_result" {
		return "", nil, false
	}
	payload, ok := event.Data.(map[string]any)
	if !ok || payload["name"] != toolpolicy.ToolRunCommand {
		return "", nil, false
	}
	toolCallID, idOK := payload["id"].(string)
	output, outputOK := payload["output"].(map[string]any)
	return toolCallID, output, idOK && outputOK
}

func waitForFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", path, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertFileKeepsChanging(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read initial heartbeat: %v", err)
	}
	for {
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read heartbeat: %v", readErr)
		}
		if !bytes.Equal(before, after) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("first command stopped while second command started: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertEngineApprovalEventOrder(
	t *testing.T,
	runEngine *Engine,
	runID string,
	approvalIDs map[string]string,
) {
	t.Helper()
	type lifecycle struct {
		requested int64
		resolved  int64
		started   int64
		output    int64
	}
	byToolCall := make(map[string]*lifecycle, len(approvalIDs))
	for toolCallID := range approvalIDs {
		byToolCall[toolCallID] = &lifecycle{}
	}
	for _, event := range engineHubEvents(t, runEngine, runID) {
		switch value := event.Data.(type) {
		case ToolApprovalRequest:
			if current := byToolCall[value.ToolCallID]; event.Type == "tool_approval" && current != nil {
				current.requested = event.Sequence
			}
		case ToolApprovalResolution:
			for toolCallID, approvalID := range approvalIDs {
				if event.Type == "tool_approval_resolved" && value.ID == approvalID {
					byToolCall[toolCallID].resolved = event.Sequence
				}
			}
		case ToolApprovalStarted:
			for toolCallID, approvalID := range approvalIDs {
				if event.Type == "tool_approval_started" && value.ID == approvalID {
					byToolCall[toolCallID].started = event.Sequence
				}
			}
		case workspacetools.CommandOutputEvent:
			if current := byToolCall[value.ToolCallID]; event.Type == "command_output" && current != nil && current.output == 0 {
				current.output = event.Sequence
			}
		}
	}
	for toolCallID, current := range byToolCall {
		if current.requested == 0 || current.resolved == 0 ||
			current.started == 0 || current.output == 0 ||
			!(current.requested < current.resolved &&
				current.resolved < current.started &&
				current.started < current.output) {
			t.Fatalf("approval lifecycle for %s = %#v", toolCallID, current)
		}
	}
	first := byToolCall["command-first"]
	second := byToolCall["command-second"]
	if first.output >= second.resolved {
		t.Fatalf(
			"first output sequence %d is not before second resolution sequence %d",
			first.output,
			second.resolved,
		)
	}
	if first.resolved <= second.requested {
		t.Fatalf(
			"first resolution sequence %d occurred before both requests surfaced (second %d)",
			first.resolved,
			second.requested,
		)
	}
}

func engineHubEvents(t *testing.T, runEngine *Engine, runID string) []StreamEvent {
	t.Helper()
	runEngine.hub.mu.Lock()
	defer runEngine.hub.mu.Unlock()
	stream := runEngine.hub.streams[runID]
	if stream == nil {
		t.Fatalf("hub stream %q does not exist", runID)
	}
	return stream.eventsAfter(0)
}

func TestEngineRunCommandHelper(t *testing.T) {
	if os.Getenv(engineCommandHelperEnvironment) != "1" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) != separator+5 || os.Args[separator+1] != "wait-file" {
		t.Fatalf("unexpected helper arguments: %q", os.Args)
	}
	name := os.Args[separator+2]
	releaseFile := os.Args[separator+3]
	heartbeatFile := os.Args[separator+4]
	fmt.Printf("started:%s\n", name)
	deadline := time.Now().Add(10 * time.Second)
	for counter := 1; ; counter++ {
		if _, err := os.Stat(releaseFile); err == nil {
			fmt.Printf("finished:%s\n", name)
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat release file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for release file %s", releaseFile)
		}
		if err := os.WriteFile(
			heartbeatFile,
			[]byte(strconv.Itoa(counter)),
			0o600,
		); err != nil {
			t.Fatalf("write heartbeat: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
