package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/acpruntime"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
)

func TestACPSessionUsagePublishesContextUpdate(t *testing.T) {
	hub := NewHub()
	hub.Create("run-usage")
	handler := &acpRunHandler{
		engine: &Engine{hub: hub},
		run:    store.Run{ID: "run-usage"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, ok := hub.Subscribe(ctx, "run-usage", 0)
	if !ok {
		t.Fatal("Hub.Subscribe() did not find the run")
	}
	if err := handler.SessionUpdate(ctx, acp.SessionNotification{
		Update: acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
			Used: 9_000,
			Size: 10_000,
			Cost: &acp.Cost{Amount: 0.42, Currency: "USD"},
		}},
	}); err != nil {
		t.Fatalf("SessionUpdate() error = %v", err)
	}
	event := <-events
	usage, ok := event.Data.(acpUsageUpdate)
	if event.Type != "acp_usage" || !ok || usage.Percentage != 90 || usage.Cost.Amount != 0.42 {
		t.Fatalf("usage event = %#v", event)
	}
}

func TestACPAdditionalDirectoriesUsesPermittedRepositoryRoot(t *testing.T) {
	repositoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repositoryRoot, ".git"), 0o755); err != nil {
		t.Fatalf("create repository marker: %v", err)
	}
	workspaceRoot := filepath.Join(repositoryRoot, "packages", "service")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	permissions := toolpolicy.DefaultPermissions()
	if got := acpAdditionalDirectories(workspaceRoot, permissions); got != nil {
		t.Fatalf("default additional directories = %#v, want nil", got)
	}
	for index := range permissions {
		if permissions[index].ToolName == toolpolicy.ToolReadFile {
			permissions[index].FilesystemScope = toolpolicy.ScopeRepository
		}
	}
	got := acpAdditionalDirectories(workspaceRoot, permissions)
	if len(got) != 1 || got[0] != repositoryRoot {
		t.Fatalf("acpAdditionalDirectories() = %#v", got)
	}
}

func TestACPTerminalOutputWaitsForToolReference(t *testing.T) {
	handler := &acpRunHandler{
		tools:            make(map[string]*acpToolState),
		terminalTools:    make(map[string]string),
		pendingTerminals: make(map[string]map[string]string),
	}
	handler.TerminalOutput(acpruntime.TerminalOutputEvent{
		TerminalID: "terminal-1",
		Stream:     "stdout",
		Text:       "first\n",
	})
	tool := &acpToolState{
		id:   "tool-1",
		kind: acp.ToolKindExecute,
		content: []acp.ToolCallContent{
			acp.ToolTerminalRef("terminal-1"),
		},
	}
	handler.tools[tool.id] = tool

	pending := handler.attachToolTerminals(tool)
	if pending["stdout"] != "first\n" {
		t.Fatalf("attachToolTerminals() = %#v", pending)
	}
	if tool.stdout != "first\n" || handler.terminalTools["terminal-1"] != tool.id {
		t.Fatalf(
			"attached terminal state = stdout %q, terminal tool %q",
			tool.stdout,
			handler.terminalTools["terminal-1"],
		)
	}
}

func TestACPTerminalOutputPublishesCommandEvent(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	handler := &acpRunHandler{
		engine: &Engine{hub: hub},
		run:    store.Run{ID: "run-1"},
		tools: map[string]*acpToolState{
			"tool-1": {id: "tool-1", kind: acp.ToolKindExecute},
		},
		terminalTools:    map[string]string{"terminal-1": "tool-1"},
		pendingTerminals: make(map[string]map[string]string),
	}

	handler.TerminalOutput(acpruntime.TerminalOutputEvent{
		TerminalID: "terminal-1",
		Stream:     "stdout",
		Text:       "live output\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, ok := hub.Subscribe(ctx, "run-1", 0)
	if !ok {
		t.Fatal("Hub.Subscribe() did not find the run")
	}
	event := <-events
	data, ok := event.Data.(map[string]string)
	if event.Type != "command_output" ||
		!ok ||
		data["toolCallId"] != "tool-1" ||
		data["stream"] != "stdout" ||
		data["text"] != "live output\n" {
		t.Fatalf("terminal stream event = %#v", event)
	}
	if handler.tools["tool-1"].stdout != "live output\n" {
		t.Fatalf("stored tool output = %q", handler.tools["tool-1"].stdout)
	}
}

func TestACPSessionUpdatePublishesTerminalOutputMetadata(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	agent, err := dataStore.CreateACPAgent(ctx, "Test agent", "go", nil)
	if err != nil {
		t.Fatalf("CreateACPAgent() error = %v", err)
	}
	session, err := dataStore.CreateACPSession(ctx, workspace.ID, "ACP output", agent.ID)
	if err != nil {
		t.Fatalf("CreateACPSession() error = %v", err)
	}
	run, err := dataStore.CreateACPRun(ctx, session.ID, "Run a command")
	if err != nil {
		t.Fatalf("CreateACPRun() error = %v", err)
	}
	runEngine := &Engine{store: dataStore, hub: NewHub()}
	runEngine.hub.Create(run.ID)
	handler := &acpRunHandler{
		engine:           runEngine,
		ctx:              ctx,
		run:              run,
		session:          session,
		segments:         make(map[string]string),
		tools:            make(map[string]*acpToolState),
		terminalTools:    make(map[string]string),
		pendingTerminals: make(map[string]map[string]string),
	}

	if err := handler.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: "acp-session",
		Update: acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
			SessionUpdate: "tool_call",
			ToolCallId:    "command-1",
			Title:         "go test ./...",
			Kind:          acp.ToolKindExecute,
			Status:        acp.ToolCallStatusInProgress,
			Content:       []acp.ToolCallContent{acp.ToolTerminalRef("command-1")},
		}},
	}); err != nil {
		t.Fatalf("initial SessionUpdate() error = %v", err)
	}
	if err := handler.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: "acp-session",
		Update: acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallId:    "command-1",
			Meta: map[string]any{
				"terminal_output_delta": map[string]any{
					"data":        "package output\n",
					"terminal_id": "command-1",
				},
			},
		}},
	}); err != nil {
		t.Fatalf("output SessionUpdate() error = %v", err)
	}

	events, ok := runEngine.hub.Subscribe(ctx, run.ID, 0)
	if !ok {
		t.Fatal("Hub.Subscribe() did not find the run")
	}
	if event := <-events; event.Type != "tool_call" {
		t.Fatalf("initial tool event = %#v", event)
	}
	statusEvent := <-events
	status, ok := statusEvent.Data.(acpToolStatusUpdate)
	if statusEvent.Type != "tool_status" || !ok ||
		status.ID != "command-1" || status.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("tool status event = %#v", statusEvent)
	}
	event := <-events
	data, ok := event.Data.(map[string]string)
	if event.Type != "command_output" ||
		!ok ||
		data["toolCallId"] != "command-1" ||
		data["stream"] != "stdout" ||
		data["text"] != "package output\n" {
		t.Fatalf("metadata stream event = %#v", event)
	}
}

func TestACPConcurrentPermissionsRemainCorrelatedAndQueued(t *testing.T) {
	runEngine, _, handler, _ := newACPInternalNotesTest(t, toolpolicy.DefaultPermissions())
	type permissionResult struct {
		toolCallID string
		optionID   acp.PermissionOptionId
		err        error
	}
	results := make(chan permissionResult, 2)
	requestPermission := func(toolCallID, optionID string) {
		go func() {
			response, err := handler.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					ToolCallId: acp.ToolCallId(toolCallID),
					Title:      acp.Ptr("Run " + toolCallID),
					Kind:       acp.Ptr(acp.ToolKindExecute),
					Status:     acp.Ptr(acp.ToolCallStatusPending),
					RawInput:   map[string]any{"command": toolCallID},
				},
				Options: []acp.PermissionOption{
					{
						OptionId: acp.PermissionOptionId(optionID),
						Name:     "Allow once",
						Kind:     acp.PermissionOptionKindAllowOnce,
					},
					{
						OptionId: acp.PermissionOptionId("reject-" + toolCallID),
						Name:     "Reject",
						Kind:     acp.PermissionOptionKindRejectOnce,
					},
				},
			})
			selected := acp.PermissionOptionId("")
			if response.Outcome.Selected != nil {
				selected = response.Outcome.Selected.OptionId
			}
			results <- permissionResult{toolCallID: toolCallID, optionID: selected, err: err}
		}()
	}
	requestPermission("command-first", "allow-first")
	requestPermission("command-second", "allow-second")

	approvals := make(map[string]ToolApprovalRequest, 2)
	deadline := time.Now().Add(2 * time.Second)
	for len(approvals) < 2 {
		runEngine.mu.Lock()
		for _, pending := range runEngine.active[handler.session.ID].pendingApprovals {
			approvals[pending.request.ToolCallID] = pending.request
		}
		runEngine.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for ACP approvals: %#v", approvals)
		}
		time.Sleep(time.Millisecond)
	}

	firstApproval := approvals["command-first"]
	if _, err := runEngine.ResolveToolApproval(
		t.Context(), handler.run.ID, firstApproval.ID, true, "", "allow-first",
	); err != nil {
		t.Fatalf("resolve first approval: %v", err)
	}
	select {
	case result := <-results:
		if result.err != nil || result.toolCallID != "command-first" || result.optionID != "allow-first" {
			t.Fatalf("first permission response = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first ACP permission response")
	}
	if err := handler.SessionUpdate(t.Context(), acp.SessionNotification{
		Update: acp.UpdateToolCall(
			"command-first",
			acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
		),
	}); err != nil {
		t.Fatalf("publish first execution status: %v", err)
	}

	secondApproval := approvals["command-second"]
	runEngine.mu.Lock()
	secondPending := runEngine.active[handler.session.ID].pendingApprovals[secondApproval.ID]
	secondUnresolved := secondPending != nil && secondPending.resolutionOrder == 0
	runEngine.mu.Unlock()
	if !secondUnresolved {
		t.Fatal("resolving the first ACP approval also resolved the second")
	}

	statuses := make(map[string]acp.ToolCallStatus)
	for _, event := range engineHubEvents(t, runEngine, handler.run.ID) {
		if event.Type == "tool_approval_started" {
			t.Fatalf("ACP permission release published an execution-start event: %#v", event)
		}
		if status, ok := event.Data.(acpToolStatusUpdate); event.Type == "tool_status" && ok {
			statuses[status.ID] = status.Status
		}
	}
	if statuses["command-first"] != acp.ToolCallStatusInProgress ||
		statuses["command-second"] != acp.ToolCallStatusPending {
		t.Fatalf("ACP tool statuses = %#v", statuses)
	}

	if _, err := runEngine.ResolveToolApproval(
		t.Context(), handler.run.ID, secondApproval.ID, true, "", "allow-second",
	); err != nil {
		t.Fatalf("resolve second approval: %v", err)
	}
	select {
	case result := <-results:
		if result.err != nil || result.toolCallID != "command-second" || result.optionID != "allow-second" {
			t.Fatalf("second permission response = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second ACP permission response")
	}
}

func TestACPToolStreamsCumulativeRawOutput(t *testing.T) {
	tool := &acpToolState{
		kind:      acp.ToolKindExecute,
		status:    acp.ToolCallStatusInProgress,
		rawOutput: map[string]any{"formatted_output": "first"},
	}
	if output := tool.streamDeltas(); output["stdout"] != "first" {
		t.Fatalf("first streamDeltas() = %#v", output)
	}
	tool.rawOutput = map[string]any{"formatted_output": "first second"}
	if output := tool.streamDeltas(); output["stdout"] != " second" {
		t.Fatalf("second streamDeltas() = %#v", output)
	}
	tool.rawOutput = map[string]any{"formatted_output": "replacement"}
	if output := tool.streamDeltas(); len(output) != 0 {
		t.Fatalf("replacement streamDeltas() = %#v, want no duplicate output", output)
	}
}

func TestACPToolDeduplicatesCumulativeTerminalMetadata(t *testing.T) {
	tool := &acpToolState{kind: acp.ToolKindExecute}
	first := tool.streamMetadataDeltas(map[string]any{
		"terminal_output": map[string]any{"data": "first"},
	})
	if first["stdout"] != "first" {
		t.Fatalf("first streamMetadataDeltas() = %#v", first)
	}
	second := tool.streamMetadataDeltas(map[string]any{
		"terminal_output": map[string]any{"data": "first second"},
	})
	if second["stdout"] != " second" {
		t.Fatalf("second streamMetadataDeltas() = %#v", second)
	}
	duplicate := tool.streamMetadataDeltas(map[string]any{
		"terminal_output": map[string]any{"data": "first second"},
	})
	if len(duplicate) != 0 || tool.stdout != "first second" {
		t.Fatalf("duplicate streamMetadataDeltas() = %#v, stdout = %q", duplicate, tool.stdout)
	}
}

func TestACPToolStateNormalizesCommandDetails(t *testing.T) {
	tool := acpToolState{
		id:     "command-1",
		title:  "Check the project",
		kind:   acp.ToolKindExecute,
		status: acp.ToolCallStatusCompleted,
		rawInput: map[string]any{
			"command": "go",
			"args":    []any{"test", "./..."},
			"cwd":     "/workspace",
		},
		rawOutput: map[string]any{
			"exit_code":        float64(0),
			"formatted_output": "ok\n",
		},
	}

	input := tool.input()
	if input["workingDirectory"] != "/workspace" || input["title"] != tool.title {
		t.Fatalf("input() = %#v", input)
	}
	output := tool.output()
	if output["exitCode"] != float64(0) ||
		output["stdout"] != "ok\n" ||
		output["state"] != string(acp.ToolCallStatusCompleted) {
		t.Fatalf("output() = %#v", output)
	}
	payload := tool.approvalPayload(store.AppSession{ACPSessionID: "session-1"})
	if payload["kind"] != "run_command" ||
		payload["workingDirectory"] != "/workspace" ||
		payload["timeoutSeconds"] != 120 ||
		payload["acpSessionId"] != "session-1" {
		t.Fatalf("approvalPayload() = %#v", payload)
	}
}

func TestACPToolStateDoesNotUseCommandTitleAsFailureError(t *testing.T) {
	tool := acpToolState{
		title:  `node -e "process.exit(1)"`,
		kind:   acp.ToolKindExecute,
		status: acp.ToolCallStatusFailed,
	}

	output := tool.output()
	if _, ok := output["error"]; ok {
		t.Fatalf("output() synthesized command error = %#v", output)
	}
	if output["state"] != string(acp.ToolCallStatusFailed) {
		t.Fatalf("output() = %#v", output)
	}

	tool.rawOutput = map[string]any{"error": "process could not be started"}
	if output := tool.output(); output["error"] != "process could not be started" {
		t.Fatalf("output() explicit error = %#v", output)
	}
}

func TestACPToolStateBuildsFileChangePreview(t *testing.T) {
	oldText := "old\n"
	tool := acpToolState{
		id:    "edit-1",
		title: "Update configuration",
		kind:  acp.ToolKindEdit,
		content: []acp.ToolCallContent{
			{
				Diff: &acp.ToolCallContentDiff{
					Path:    "config.txt",
					OldText: &oldText,
					NewText: "new\n",
				},
			},
			{
				Diff: &acp.ToolCallContentDiff{
					Path:    "created.txt",
					NewText: "created\n",
				},
			},
		},
	}

	files := tool.diffFiles()
	if len(files) != 2 {
		t.Fatalf("diffFiles() = %#v", files)
	}
	if files[0]["operation"] != "update" ||
		files[0]["path"] != "config.txt" ||
		files[1]["operation"] != "create" ||
		files[1]["path"] != "created.txt" {
		t.Fatalf("diffFiles() = %#v", files)
	}
	payload := tool.approvalPayload(store.AppSession{ACPSessionID: "session-1"})
	if payload["kind"] != "file_edit" {
		t.Fatalf("approvalPayload() kind = %#v", payload["kind"])
	}
	payloadFiles, ok := payload["files"].([]map[string]any)
	if !ok || len(payloadFiles) != 2 {
		t.Fatalf("approvalPayload() files = %#v", payload["files"])
	}
}

func TestDefaultACPPermissionOption(t *testing.T) {
	options := []acp.PermissionOption{
		{
			OptionId: "allow-always",
			Name:     "Allow for session",
			Kind:     acp.PermissionOptionKindAllowAlways,
		},
		{
			OptionId: "allow-once",
			Name:     "Allow once",
			Kind:     acp.PermissionOptionKindAllowOnce,
		},
		{
			OptionId: "reject",
			Name:     "Reject",
			Kind:     acp.PermissionOptionKindRejectOnce,
		},
	}

	if got := defaultPermissionOption(options, true); got != "allow-once" {
		t.Fatalf("defaultPermissionOption(approved) = %q, want allow-once", got)
	}
	if got := defaultPermissionOption(options, false); got != "reject" {
		t.Fatalf("defaultPermissionOption(rejected) = %q, want reject", got)
	}
}

func TestMergeACPConfigOptionsPreservesUserSelections(t *testing.T) {
	preferred := []acp.SessionConfigOption{
		acpSelectConfigOption(
			"model",
			acp.SessionConfigOptionCategoryModel,
			"frontier",
			"balanced",
			"frontier",
		),
		acpBooleanConfigOption("fast", false),
		acpSelectConfigOption(
			"removed-value",
			acp.SessionConfigOptionCategoryThoughtLevel,
			"ultra",
			"high",
			"ultra",
		),
	}
	reported := []acp.SessionConfigOption{
		acpSelectConfigOption(
			"model",
			acp.SessionConfigOptionCategoryModel,
			"balanced",
			"balanced",
			"frontier",
		),
		acpBooleanConfigOption("fast", true),
		acpSelectConfigOption(
			"removed-value",
			acp.SessionConfigOptionCategoryThoughtLevel,
			"high",
			"high",
		),
	}

	merged := mergeACPConfigOptions(preferred, reported)

	if merged[0].Select == nil || merged[0].Select.CurrentValue != "frontier" {
		t.Fatalf("merged model = %#v", merged[0])
	}
	if merged[1].Boolean == nil || merged[1].Boolean.CurrentValue {
		t.Fatalf("merged boolean = %#v", merged[1])
	}
	if merged[2].Select == nil || merged[2].Select.CurrentValue != "high" {
		t.Fatalf("merged unavailable value = %#v", merged[2])
	}
	if reported[0].Select.CurrentValue != "balanced" || !reported[1].Boolean.CurrentValue {
		t.Fatalf("merge mutated reported options: %#v", reported)
	}
}

func TestACPPlanUpdatePreservesEntryState(t *testing.T) {
	plan := newACPPlanUpdate("run-1", []acp.PlanEntry{
		{
			Content:  "Inspect the implementation",
			Priority: acp.PlanEntryPriorityHigh,
			Status:   acp.PlanEntryStatusCompleted,
		},
		{
			Content:  "Update the activity timeline",
			Priority: acp.PlanEntryPriorityMedium,
			Status:   acp.PlanEntryStatusInProgress,
		},
	})

	if plan.ID != "run-1:plan" || len(plan.Entries) != 2 {
		t.Fatalf("newACPPlanUpdate() = %#v", plan)
	}
	if plan.Entries[0].Priority != string(acp.PlanEntryPriorityHigh) ||
		plan.Entries[0].Status != string(acp.PlanEntryStatusCompleted) ||
		plan.Entries[1].Content != "Update the activity timeline" ||
		plan.Entries[1].Status != string(acp.PlanEntryStatusInProgress) {
		t.Fatalf("newACPPlanUpdate() entries = %#v", plan.Entries)
	}
}

func acpSelectConfigOption(
	id string,
	category acp.SessionConfigOptionCategory,
	current string,
	values ...string,
) acp.SessionConfigOption {
	selectValues := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(values))
	for _, value := range values {
		selectValues = append(selectValues, acp.SessionConfigSelectOption{
			Name:  value,
			Value: acp.SessionConfigValueId(value),
		})
	}
	option := acp.NewSessionConfigOptionSelect(
		acp.SessionConfigValueId(current),
		acp.SessionConfigSelectOptions{Ungrouped: &selectValues},
	)
	option.Select.Id = acp.SessionConfigId(id)
	option.Select.Name = id
	option.Select.Category = &category
	return option
}

func acpBooleanConfigOption(id string, current bool) acp.SessionConfigOption {
	option := acp.NewSessionConfigOptionBoolean(current)
	option.Boolean.Id = acp.SessionConfigId(id)
	option.Boolean.Name = id
	return option
}
