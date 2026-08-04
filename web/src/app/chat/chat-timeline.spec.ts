import { TranscriptItem } from '../core/models';
import {
  applyLiveContextCompaction,
  applyLiveACPElicitationCompletion,
  applyLiveMCPCallFinished,
  applyLiveMCPCallStarted,
  applyLiveMCPLog,
  applyLiveMCPElicitationRequest,
  applyLiveMCPElicitationResolution,
  applyLiveMCPProgress,
  appendLiveCommandOutput,
  buildChatTimeline,
  buildLiveActivitySteps,
  buildLiveChatTimeline,
  buildLiveToolApproval,
  buildLiveUserInput,
  effectiveDelegationStatus,
  latestAgentPlan,
  transcriptBeforeActiveInvocation,
} from './chat-timeline';

describe('chat timeline', () => {
  it('keeps an MCP elicitation on its tool step until it is resolved', () => {
    const started = applyLiveMCPCallStarted([], {
      sessionId: 'session-1',
      toolCallId: 'tool-1',
      serverId: 'server-1',
      serverName: 'Project server',
      toolName: 'create_issue',
      cancelable: true,
    });
    const waiting = applyLiveMCPElicitationRequest(started, {
      id: 'elicitation-1',
      sessionId: 'session-1',
      toolCallId: 'tool-1',
      serverId: 'server-1',
      serverName: 'Project server',
      mode: 'form',
      message: 'Choose a project',
      requestedSchema: { type: 'object' },
    });

    expect(buildLiveActivitySteps(waiting)).toMatchObject([
      {
        id: 'tool-1',
        status: 'input_required',
        mcpElicitation: { id: 'elicitation-1', status: 'pending' },
      },
    ]);

    const resolved = applyLiveMCPElicitationResolution(waiting, {
      id: 'elicitation-1',
      toolCallId: 'tool-1',
      action: 'accept',
      content: { project: 'MM' },
    });
    expect(buildLiveActivitySteps(resolved)).toMatchObject([
      {
        id: 'tool-1',
        status: 'running',
        mcpElicitation: { status: 'resolved', resolution: { action: 'accept' } },
      },
    ]);
  });

  it('shows an ACP elicitation as protocol-neutral user input', () => {
    const waiting = applyLiveMCPElicitationRequest([], {
      id: 'elicitation-1',
      source: 'acp',
      sessionId: 'session-1',
      toolCallId: 'acp-elicitation:1',
      serverId: 'agent-1',
      serverName: 'Codex',
      mode: 'form',
      message: 'Choose a target',
      requestedSchema: { type: 'object' },
    });

    expect(buildLiveActivitySteps(waiting)).toMatchObject([
      {
        toolName: 'acp_elicitation',
        label: 'Request user input',
        icon: 'contact_support',
        status: 'input_required',
        mcp: undefined,
      },
    ]);

    const completed = applyLiveACPElicitationCompletion(waiting, {
      id: 'elicitation-1',
      elicitationId: 'login-1',
    });
    expect(buildLiveActivitySteps(completed)).toMatchObject([
      { mcpElicitation: { externalCompleted: true } },
    ]);
  });

  it('maps session note reads and updates to dedicated timeline steps', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'read-notes',
        name: 'read_session_notes',
        input: {},
        output: {
          state: 'read',
          content: '# Decisions\n\n- Keep the API stable.',
          revision: 2,
        },
      },
      {
        kind: 'tool',
        id: 'update-notes',
        name: 'update_session_notes',
        input: {
          content: '# Decisions\n\n- Keep the API stable.\n- Add tests.',
          expectedRevision: 2,
        },
        output: { state: 'updated', revision: 3, bytes: 53 },
      },
    ]);

    expect(steps).toMatchObject([
      {
        toolName: 'read_session_notes',
        label: 'Read session notes',
        icon: 'note_alt',
        status: 'complete',
        sessionNotes: {
          operation: 'read',
          revision: 2,
          content: '# Decisions\n\n- Keep the API stable.',
        },
      },
      {
        toolName: 'update_session_notes',
        label: 'Updated session notes',
        icon: 'edit_note',
        status: 'complete',
        sessionNotes: {
          operation: 'update',
          revision: 3,
          expectedRevision: 2,
          bytes: 53,
          content: '# Decisions\n\n- Keep the API stable.\n- Add tests.',
        },
      },
    ]);
  });

  it('shows a dedicated approval prompt for session note updates', () => {
    const approval = buildLiveToolApproval({
      id: 'approval-1',
      toolCallId: 'update-notes',
      toolName: 'update_session_notes',
      input: { content: '# Notes', expectedRevision: 1 },
      payload: { kind: 'session_notes', operation: 'update', expectedRevision: 1 },
    });

    expect(approval).toMatchObject({
      kind: 'session_notes',
      prompt: 'Allow the agent to update the session notes?',
      icon: 'edit_note',
      targetLabel: 'Scope',
      target: 'Current session',
      status: 'pending',
    });
  });

  it('shows live context compaction and updates the same activity when it completes', () => {
    const running = applyLiveContextCompaction([], {
      id: 'context-compaction:invocation-1:8',
      status: 'running',
      estimatedTokensBefore: 115_000,
      maxContextTokens: 128_000,
      summarizedContents: 8,
    });

    expect(buildLiveChatTimeline(running)).toMatchObject([
      {
        kind: 'activity',
        steps: [
          {
            id: 'context-compaction:invocation-1:8',
            toolName: 'context_compaction',
            label: 'Compacting context',
            icon: 'compress',
            status: 'running',
          },
        ],
      },
    ]);

    const completed = applyLiveContextCompaction(running, {
      id: 'context-compaction:invocation-1:8',
      status: 'completed',
      estimatedTokensBefore: 115_000,
      estimatedTokensAfter: 36_000,
      maxContextTokens: 128_000,
      summarizedContents: 8,
    });

    expect(completed).toHaveLength(1);
    expect(buildLiveChatTimeline(completed)).toMatchObject([
      {
        kind: 'activity',
        steps: [
          {
            label: 'Context compacted',
            status: 'complete',
            output: { state: 'completed', estimatedTokensAfter: 36_000 },
          },
        ],
      },
    ]);
  });

  it('restores a persisted context compaction in the conversation timeline', () => {
    const transcript: TranscriptItem[] = [
      message('user', 'Continue the task', 'user'),
      {
        id: 'context-compaction:invocation-1:8',
        invocationId: 'invocation-1',
        kind: 'context_compaction',
        toolName: 'context_compaction',
        toolInput: {
          title: 'Compact context',
          estimatedTokensBefore: 115_000,
          maxContextTokens: 128_000,
          summarizedContents: 8,
        },
        toolOutput: { state: 'completed', estimatedTokensAfter: 36_000 },
        createdAt: '2026-07-21T08:00:01Z',
      },
      message('answer', 'Done.', 'assistant'),
    ];

    expect(buildChatTimeline(transcript)).toMatchObject([
      { kind: 'message' },
      {
        kind: 'activity',
        steps: [
          {
            toolName: 'context_compaction',
            label: 'Context compacted',
            icon: 'compress',
            status: 'complete',
          },
        ],
      },
      { kind: 'message' },
    ]);
  });

  it('keeps the active invocation out of persisted history while it is streamed', () => {
    const transcript: TranscriptItem[] = [
      {
        ...message('previous-user', 'Review the workspace', 'user'),
        invocationId: 'previous-invocation',
      },
      {
        ...message('active-user', 'Continue the review', 'user'),
        invocationId: 'active-invocation',
      },
      {
        id: 'active-delegation',
        invocationId: 'active-invocation',
        kind: 'subagent_call',
        agentName: 'code_reviewer',
        agentLabel: 'Code reviewer',
        toolName: 'code_reviewer',
        toolCallId: 'delegation-1',
        toolInput: { request: 'Review the current changes.' },
        createdAt: '2026-07-21T08:00:01Z',
      },
    ];

    expect(
      transcriptBeforeActiveInvocation(transcript, 'active-invocation').map((item) => item.id),
    ).toEqual(['previous-user']);
    expect(transcriptBeforeActiveInvocation(transcript)).toBe(transcript);
  });

  it('reconstructs delegated agent work as a nested timeline', () => {
    const transcript: TranscriptItem[] = [
      message('user', 'Find where sessions are stored', 'user'),
      {
        id: 'delegate',
        invocationId: 'invocation-1',
        kind: 'subagent_call',
        agentName: 'workspace_explorer',
        agentLabel: 'Workspace explorer',
        toolName: 'workspace_explorer',
        toolCallId: 'delegation-1',
        toolInput: { request: 'Locate the session persistence implementation.' },
        createdAt: '2026-07-21T08:00:01Z',
      },
      {
        ...toolCall('child-read', 'read-1', 'read_file', 'internal/store/store.go'),
        agentName: 'workspace_explorer',
        agentLabel: 'Workspace explorer',
        agentPath: 'workspace_agent.workspace_explorer',
        delegationId: 'delegation-1',
      },
      {
        ...toolResult('child-result', 'read-1', 'read_file', { content: 'package store' }),
        agentName: 'workspace_explorer',
        agentLabel: 'Workspace explorer',
        agentPath: 'workspace_agent.workspace_explorer',
        delegationId: 'delegation-1',
      },
      {
        id: 'delegate-result',
        invocationId: 'invocation-1',
        kind: 'subagent_result',
        agentName: 'workspace_explorer',
        agentLabel: 'Workspace explorer',
        toolName: 'workspace_explorer',
        toolCallId: 'delegation-1',
        toolOutput: { result: 'Sessions are persisted in `internal/store`.' },
        createdAt: '2026-07-21T08:00:04Z',
      },
      message('answer', 'The session store is in `internal/store`.', 'assistant'),
    ];

    const timeline = buildChatTimeline(transcript);

    expect(timeline.map((item) => item.kind)).toEqual(['message', 'subagent', 'message']);
    expect(timeline[1]).toMatchObject({
      kind: 'subagent',
      delegation: {
        id: 'delegation-1',
        name: 'workspace_explorer',
        label: 'Workspace explorer',
        task: 'Locate the session persistence implementation.',
        status: 'complete',
        result: 'Sessions are persisted in `internal/store`.',
        timeline: [
          {
            kind: 'activity',
            steps: [
              {
                toolName: 'read_file',
                label: 'Read internal/store/store.go',
                status: 'complete',
              },
            ],
          },
        ],
      },
    });
  });

  it('keeps live child tool state inside its delegated agent', () => {
    const items = appendLiveCommandOutput(
      [
        {
          kind: 'subagent',
          id: 'delegation-1',
          name: 'workspace_explorer',
          label: 'Workspace explorer',
          task: 'Inspect the tests.',
          status: 'running',
          activities: [
            {
              kind: 'tool',
              id: 'command-1',
              name: 'run_command',
              input: { command: 'go', args: ['test', './...'] },
            },
          ],
        },
      ],
      { toolCallId: 'command-1', stream: 'stdout', text: 'ok\n' },
    );

    const timeline = buildLiveChatTimeline(items);

    expect(timeline).toMatchObject([
      {
        kind: 'subagent',
        delegation: {
          id: 'delegation-1',
          status: 'running',
          timeline: [
            {
              kind: 'activity',
              running: true,
              steps: [{ command: { stdout: 'ok\n' } }],
            },
          ],
        },
      },
    ]);
  });

  it('prioritizes delegated approval, then an explicit terminal state over stale activity', () => {
    const delegation = {
      id: 'delegation-1',
      name: 'code_reviewer',
      label: 'Correctness reviewer',
      task: 'Review the change.',
      status: 'complete' as const,
      timeline: [
        {
          kind: 'activity' as const,
          id: 'activity-1',
          steps: [
            {
              id: 'complete-step',
              label: 'Read the change',
              icon: 'description',
              status: 'complete' as const,
            },
            {
              id: 'running-step',
              label: 'Inspect callers',
              icon: 'search',
              status: 'running' as const,
            },
            {
              id: 'approval-step',
              label: 'Run repository tool',
              icon: 'terminal',
              status: 'approval_required' as const,
            },
          ],
        },
      ],
    };

    expect(effectiveDelegationStatus(delegation)).toBe('approval_required');
    expect(
      effectiveDelegationStatus({
        ...delegation,
        timeline: [
          {
            ...delegation.timeline[0],
            steps: delegation.timeline[0].steps.slice(0, 2),
          },
        ],
      }),
    ).toBe('complete');
    expect(
      effectiveDelegationStatus({
        ...delegation,
        status: 'running',
        timeline: [
          {
            ...delegation.timeline[0],
            steps: delegation.timeline[0].steps.slice(0, 2),
          },
        ],
      }),
    ).toBe('running');
  });

  it('keeps parallel subagent completion independent from another running subagent', () => {
    const timeline = buildLiveChatTimeline([
      {
        kind: 'subagent',
        id: 'completed-reviewer',
        name: 'code_reviewer',
        label: 'Completed reviewer',
        task: 'Review correctness.',
        status: 'complete',
        output: { result: 'Review complete.' },
        activities: [
          {
            kind: 'tool',
            id: 'stale-tool',
            name: 'read_file',
            input: { path: 'main.go' },
          },
        ],
      },
      {
        kind: 'subagent',
        id: 'running-reviewer',
        name: 'code_reviewer',
        label: 'Running reviewer',
        task: 'Review tests.',
        status: 'running',
        activities: [],
      },
    ]);

    expect(
      timeline.filter((entry) => entry.kind === 'subagent').map((entry) => entry.delegation.status),
    ).toEqual(['complete', 'running']);
  });

  it('ends an activity group when the agent writes a note', () => {
    const transcript: TranscriptItem[] = [
      message('user', 'Find the Go files', 'user'),
      toolCall('list', 'list-call', 'list_directory', '.'),
      toolResult('list-result', 'list-call', 'list_directory', { entries: ['go.mod'] }),
      message('note', 'I found a module. I will inspect it.', 'assistant'),
      toolCall('read', 'read-call', 'read_file', 'go.mod'),
      toolResult('read-result', 'read-call', 'read_file', { content: 'module example' }),
      message('answer', '**Yes.** There are Go files.', 'assistant'),
    ];

    const timeline = buildChatTimeline(transcript);

    expect(timeline.map((item) => item.kind)).toEqual([
      'message',
      'activity',
      'note',
      'activity',
      'message',
    ]);
    const activity = timeline[1];
    expect(activity.kind).toBe('activity');
    if (activity.kind !== 'activity') {
      throw new Error('expected activity entry');
    }
    expect(activity.steps.map((step) => step.icon)).toEqual(['folder_open']);
    expect(activity.steps[0].output).toEqual({ entries: ['go.mod'] });
    expect(activity.steps[0].toolName).toBe('list_directory');
    expect(timeline[2]).toMatchObject({
      kind: 'note',
      text: 'I found a module. I will inspect it.',
    });
    expect(timeline[3]).toMatchObject({
      kind: 'activity',
      steps: [{ icon: 'description', toolName: 'read_file' }],
    });
    expect(timeline[4]).toMatchObject({ kind: 'message', message: { id: 'answer' } });
  });

  it('ends a live activity group when the agent writes a note', () => {
    const timeline = buildLiveChatTimeline([
      {
        kind: 'tool',
        id: 'complete',
        name: 'read_file',
        input: { path: 'go.mod' },
        output: { content: 'module example' },
      },
      { kind: 'note', id: 'note', text: 'Checking the next directory.' },
      {
        kind: 'tool',
        id: 'running',
        name: 'list_directory',
        input: { path: 'internal' },
      },
    ]);

    expect(timeline.map((item) => item.kind)).toEqual(['activity', 'note', 'activity']);
    expect(timeline[0]).toMatchObject({ kind: 'activity', running: false });
    expect(timeline[1]).toMatchObject({ kind: 'note', text: 'Checking the next directory.' });
    expect(timeline[2]).toMatchObject({
      kind: 'activity',
      running: true,
      steps: [{ status: 'running', icon: 'folder_open', label: 'Inspect internal' }],
    });
  });

  it('maps a live clarification request to a required-input step', () => {
    const questions = [
      {
        id: 'scope',
        question: 'Which package should be changed?',
        options: [
          {
            id: 'api',
            label: 'API package',
            description: 'Only change the public API package.',
          },
        ],
      },
    ];
    const userInput = buildLiveUserInput({
      id: 'input-1',
      toolCallId: 'ask-call',
      toolName: 'ask_user',
      questions,
    });

    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'ask-call',
        name: 'ask_user',
        input: { questions },
        userInput,
      },
    ]);

    expect(steps[0]).toMatchObject({
      label: 'Ask: Which package should be changed?',
      icon: 'question_answer',
      status: 'input_required',
      userInput: {
        id: 'input-1',
        status: 'pending',
        questions,
      },
    });
  });

  it('reconstructs completed clarification answers from the transcript payload', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'ask-call',
        name: 'ask_user',
        input: {
          questions: [
            {
              id: 'scope',
              question: 'Which package should be changed?',
              options: [{ id: 'api', label: 'API package' }],
            },
          ],
        },
        output: {
          state: 'answered',
          answers: [{ questionId: 'scope', optionId: 'api', answer: 'API package' }],
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      status: 'complete',
      userInput: {
        status: 'answered',
        answers: [{ questionId: 'scope', optionId: 'api', answer: 'API package' }],
      },
    });
  });

  it('maps denied and failed tool results to distinct states', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'denied',
        name: 'fetch_url',
        input: { url: 'https://example.com' },
        output: { state: 'denied' },
      },
      {
        kind: 'tool',
        id: 'timed-out',
        name: 'run_command',
        input: { command: 'go' },
        output: { state: 'timed_out', timedOut: true },
      },
      {
        kind: 'tool',
        id: 'nonzero',
        name: 'run_command',
        input: { command: 'go' },
        output: { state: 'completed', exitCode: 1 },
      },
      {
        kind: 'tool',
        id: 'http-error',
        name: 'fetch_url',
        input: { url: 'https://example.com/missing' },
        output: { state: 'fetched', httpStatus: 404 },
      },
    ]);

    expect(steps.map((step) => step.status)).toEqual(['denied', 'failed', 'failed', 'failed']);
  });

  it('uses the persisted run status when a tool has no result', () => {
    const transcript: TranscriptItem[] = [
      message('cancel-user', 'Run the command', 'user'),
      toolCall('cancel-tool', 'cancel-call', 'run_command', '.'),
    ];

    const timeline = buildChatTimeline(transcript, [
      { invocationId: 'invocation-1', status: 'cancelled' },
    ]);
    const activity = timeline.find((item) => item.kind === 'activity');

    expect(activity?.kind).toBe('activity');
    if (activity?.kind !== 'activity') {
      throw new Error('expected activity entry');
    }
    expect(activity.steps[0].status).toBe('cancelled');
  });

  it('marks a fetch as requiring approval and exposes the requested URL', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'fetch',
        name: 'fetch_url',
        input: { url: 'https://example.com/docs' },
        approval: {
          id: 'approval-1',
          kind: 'fetch_url',
          prompt: 'Allow this fetch request?',
          icon: 'language',
          targetLabel: 'URL',
          target: 'https://example.com/docs',
          status: 'pending',
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      label: 'Fetch example.com',
      toolName: 'fetch_url',
      icon: 'language',
      status: 'approval_required',
      approval: { id: 'approval-1', target: 'https://example.com/docs' },
    });
  });

  it('shows an MCP approval with the original server and tool names', () => {
    const approval = buildLiveToolApproval({
      id: 'approval-mcp',
      toolCallId: 'mcp-call',
      toolName: 'mcp_issue_tracker_create_issue_12345678',
      input: { title: 'Investigate failure' },
      hint: 'Allow Issue tracker to run create_issue?',
      payload: {
        kind: 'mcp_tool',
        serverId: 'server-1',
        serverName: 'Issue tracker',
        toolName: 'create_issue',
        namespacedToolName: 'mcp_issue_tracker_create_issue_12345678',
      },
    });
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'mcp-call',
        name: 'mcp_issue_tracker_create_issue_12345678',
        input: { title: 'Investigate failure' },
        approval,
      },
    ]);

    expect(approval).toMatchObject({
      kind: 'mcp_tool',
      icon: 'extension',
      targetLabel: 'Server and tool',
      target: 'Issue tracker / create_issue',
    });
    expect(steps[0]).toMatchObject({
      label: 'Run Issue tracker / create_issue',
      icon: 'extension',
      status: 'approval_required',
    });
  });

  it('correlates MCP progress and logs with the live tool call', () => {
    let items = applyLiveMCPCallStarted(
      [
        {
          kind: 'tool',
          id: 'mcp-call',
          name: 'mcp_artifacts_inspect',
          input: { path: '/artifact.txt' },
        },
      ],
      {
        sessionId: 'session-1',
        toolCallId: 'mcp-call',
        serverId: 'server-1',
        serverName: 'Artifacts',
        toolName: 'inspect',
        toolTitle: 'Inspect artifact',
        cancelable: true,
      },
    );
    items = applyLiveMCPProgress(items, {
      sessionId: 'session-1',
      toolCallId: 'mcp-call',
      serverId: 'server-1',
      serverName: 'Artifacts',
      toolName: 'inspect',
      toolTitle: 'Inspect artifact',
      cancelable: true,
      message: 'Reading artifact',
      progress: 1,
      total: 2,
    });
    const logged = applyLiveMCPLog(items, {
      sessionId: 'session-1',
      toolCallId: 'mcp-call',
      serverId: 'server-1',
      serverName: 'Artifacts',
      toolName: 'inspect',
      toolTitle: 'Inspect artifact',
      level: 'info',
      logger: 'reader',
      data: { phase: 'read' },
    });

    expect(logged.matched).toBe(true);
    expect(buildLiveActivitySteps(logged.items)[0]).toMatchObject({
      label: 'Run Inspect artifact',
      icon: 'extension',
      status: 'running',
      mcp: {
        serverName: 'Artifacts',
        toolName: 'inspect',
        cancelable: true,
        message: 'Reading artifact',
        progress: 1,
        total: 2,
        logs: [{ level: 'info', logger: 'reader', data: { phase: 'read' } }],
      },
    });
  });

  it('finishes an MCP call independently while sibling subagents keep running', () => {
    const items = applyLiveMCPCallFinished(
      [
        {
          kind: 'subagent',
          id: 'reviewer',
          name: 'code_reviewer',
          label: 'Correctness reviewer',
          task: 'Review the change',
          status: 'running',
          activities: [],
        },
        {
          kind: 'tool',
          id: 'mcp-call',
          name: 'mcp_issue_tracker_search',
          input: { query: 'project = TEST' },
          mcp: {
            serverId: 'server-1',
            serverName: 'Issue tracker',
            toolName: 'search',
            cancelable: true,
            cancelling: false,
            logs: [],
            content: [],
            isError: false,
          },
        },
      ],
      {
        sessionId: 'session-1',
        toolCallId: 'mcp-call',
        serverId: 'server-1',
        serverName: 'Issue tracker',
        toolName: 'search',
        cancelable: false,
        output: {
          state: 'completed',
          mcp: {
            serverId: 'server-1',
            serverName: 'Issue tracker',
            toolName: 'search',
            isError: false,
          },
          content: [{ type: 'text', text: 'One result' }],
        },
      },
    );

    const timeline = buildLiveChatTimeline(items);
    expect(timeline[0]).toMatchObject({
      kind: 'subagent',
      delegation: { id: 'reviewer', status: 'running' },
    });
    expect(timeline[1]).toMatchObject({
      kind: 'activity',
      running: false,
      steps: [
        {
          id: 'mcp-call',
          status: 'complete',
          mcp: {
            cancelable: false,
            content: [{ type: 'text', text: 'One result' }],
          },
        },
      ],
    });
  });

  it('preserves rich MCP content from a completed transcript result', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'mcp-call',
        name: 'mcp_artifacts_inspect',
        input: { path: '/artifact.txt' },
        output: {
          state: 'completed',
          mcp: {
            serverId: 'server-1',
            serverName: 'Artifacts',
            toolName: 'inspect',
            toolTitle: 'Inspect artifact',
            uiResourceUri: 'ui://artifacts/preview',
            isError: false,
          },
          content: [
            { type: 'text', text: 'Artifact ready' },
            { type: 'resource_link', uri: 'https://example.test/artifact', name: 'artifact' },
          ],
          structuredContent: { count: 2 },
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      status: 'complete',
      label: 'Run Inspect artifact',
      mcp: {
        cancelable: false,
        uiResourceUri: 'ui://artifacts/preview',
        content: [
          { type: 'text', text: 'Artifact ready' },
          { type: 'resource_link', uri: 'https://example.test/artifact', name: 'artifact' },
        ],
        structuredContent: { count: 2 },
      },
    });
  });

  it('clears live MCP progress and exposes a timeout failure', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'mcp-call',
        name: 'mcp_artifacts_inspect',
        input: {},
        output: {
          state: 'timed_out',
          timedOut: true,
          error: 'MCP tool call timed out after 2m0s',
          mcp: {
            serverId: 'server-1',
            serverName: 'Artifacts',
            toolName: 'inspect',
            isError: true,
          },
        },
        mcp: {
          serverId: 'server-1',
          serverName: 'Artifacts',
          toolName: 'inspect',
          cancelable: true,
          cancelling: false,
          message: 'Reading artifact',
          progress: 1,
          total: 2,
          logs: [],
          content: [],
          isError: false,
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      status: 'failed',
      mcp: {
        cancelable: false,
        isError: true,
        error: 'MCP tool call timed out after 2m0s',
      },
    });
    expect(steps[0].mcp?.message).toBeUndefined();
    expect(steps[0].mcp?.progress).toBeUndefined();
    expect(steps[0].mcp?.total).toBeUndefined();
  });

  it('distinguishes an approved queued tool from one that started executing', () => {
    const approval = {
      id: 'approval-1',
      kind: 'fetch_url' as const,
      prompt: 'Allow this fetch request?',
      icon: 'language',
      targetLabel: 'URL',
      target: 'https://example.com/docs',
      status: 'approved' as const,
    };
    const queued = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'fetch',
        name: 'fetch_url',
        input: { url: 'https://example.com/docs' },
        approval,
      },
    ]);
    const executing = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'fetch',
        name: 'fetch_url',
        input: { url: 'https://example.com/docs' },
        approval: { ...approval, status: 'executing' },
      },
    ]);

    expect(queued[0].status).toBe('queued');
    expect(executing[0].status).toBe('running');
  });

  it('keeps only display metadata from a completed fetch result', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'fetch',
        name: 'fetch_url',
        input: { url: 'https://example.com/docs' },
        output: {
          state: 'fetched',
          url: 'https://example.com/docs',
          finalUrl: 'https://www.example.com/docs',
          httpStatus: 200,
          contentType: 'text/html; charset=utf-8',
          content: '<html>large response</html>',
          truncated: true,
        },
      },
    ]);

    expect(steps[0].fetch).toEqual({
      state: 'fetched',
      requestedUrl: 'https://example.com/docs',
      finalUrl: 'https://www.example.com/docs',
      httpStatus: 200,
      contentType: 'text/html; charset=utf-8',
      truncated: true,
      reason: undefined,
    });
    expect(steps[0].fetch).not.toHaveProperty('content');
  });

  it('labels file searches and ranged reads', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'grep',
        name: 'grep',
        input: { pattern: 'NewSession', path: 'internal' },
        output: { matches: [] },
      },
      {
        kind: 'tool',
        id: 'read',
        name: 'read_file',
        input: { path: 'internal/session.go', startLine: 120, endLine: 180 },
        output: { content: '' },
      },
    ]);

    expect(steps).toMatchObject([
      {
        toolName: 'grep',
        label: 'Search "NewSession" in internal',
        icon: 'search',
        status: 'complete',
      },
      {
        toolName: 'read_file',
        label: 'Read internal/session.go lines 120-180',
        icon: 'description',
        status: 'complete',
      },
    ]);
  });

  it('builds a file edit approval from the validated backend preview', () => {
    const diff = '--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n';

    const approval = buildLiveToolApproval({
      id: 'approval-edit',
      toolCallId: 'edit-call',
      toolName: 'edit_file',
      input: { path: 'untrusted.go' },
      payload: { kind: 'file_edit', path: 'main.go', diff },
    });

    expect(approval).toEqual({
      id: 'approval-edit',
      kind: 'file_edit',
      prompt: 'Allow this file change?',
      icon: 'edit_note',
      targetLabel: 'File',
      target: 'main.go',
      diff,
      files: [{ operation: 'update', path: 'main.go', diff }],
      hint: undefined,
      status: 'pending',
    });
    expect(
      buildLiveActivitySteps([
        {
          kind: 'tool',
          id: 'edit-call',
          name: 'edit_file',
          input: { path: 'main.go' },
          approval,
        },
      ])[0],
    ).toMatchObject({ label: 'Edit main.go', icon: 'edit', status: 'approval_required' });
  });

  it('builds a filesystem approval with the resolved path', () => {
    const approval = buildLiveToolApproval({
      id: 'approval-read',
      toolCallId: 'read-call',
      toolName: 'read_file',
      input: { path: '../go.work' },
      payload: {
        kind: 'filesystem_access',
        operation: 'read',
        path: '../go.work',
        absolutePath: '/home/user/repo/go.work',
      },
    });

    expect(approval).toEqual({
      id: 'approval-read',
      kind: 'filesystem_access',
      prompt: 'Allow this file read?',
      icon: 'description',
      targetLabel: 'Path',
      target: '/home/user/repo/go.work',
      hint: undefined,
      status: 'pending',
    });
  });

  it('builds a file search approval with the resolved path', () => {
    const approval = buildLiveToolApproval({
      id: 'approval-grep',
      toolCallId: 'grep-call',
      toolName: 'grep',
      input: { pattern: 'TODO', path: 'internal' },
      payload: {
        kind: 'filesystem_access',
        operation: 'search',
        path: 'internal',
        absolutePath: '/home/user/repo/internal',
      },
    });

    expect(approval).toEqual({
      id: 'approval-grep',
      kind: 'filesystem_access',
      prompt: 'Allow this file search?',
      icon: 'search',
      targetLabel: 'Path',
      target: '/home/user/repo/internal',
      hint: undefined,
      status: 'pending',
    });
  });

  it('builds a skill resource approval and activity label', () => {
    const input = { name: 'go-best-practice', resource: 'references/testing.md' };
    const approval = buildLiveToolApproval({
      id: 'approval-skill',
      toolCallId: 'skill-call',
      toolName: 'load_skill',
      input,
      payload: { kind: 'skill_load', ...input },
    });

    expect(approval).toEqual({
      id: 'approval-skill',
      kind: 'skill_load',
      prompt: 'Allow this skill resource to be loaded?',
      icon: 'auto_stories',
      targetLabel: 'Resource',
      target: 'go-best-practice / references/testing.md',
      hint: undefined,
      status: 'pending',
    });
    expect(
      buildLiveActivitySteps([
        {
          kind: 'tool',
          id: 'skill-call',
          name: 'load_skill',
          input,
          approval,
        },
      ])[0],
    ).toMatchObject({
      label: 'Load references/testing.md from go-best-practice',
      icon: 'auto_stories',
      status: 'approval_required',
    });
  });

  it('builds one approval and tool step for a multi-file patch', () => {
    const files = [
      {
        operation: 'create',
        path: 'created.txt',
        diff: '--- /dev/null\n+++ b/created.txt\n@@ -0,0 +1 @@\n+created\n',
      },
      {
        operation: 'update',
        path: 'main.go',
        diff: '--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n',
      },
      {
        operation: 'delete',
        path: 'obsolete.txt',
        diff: '--- a/obsolete.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n',
      },
    ];
    const input = {
      changes: files.map(({ operation, path }) => ({ operation, path })),
    };
    const approval = buildLiveToolApproval({
      id: 'approval-batch',
      toolCallId: 'batch-call',
      toolName: 'edit_file',
      input,
      payload: { kind: 'file_patch', diff: files.map((file) => file.diff).join('\n'), files },
    });

    expect(approval).toMatchObject({
      kind: 'file_edit',
      prompt: 'Allow these file changes?',
      icon: 'difference',
      targetLabel: 'Files',
      target: '3 files',
      files,
    });
    expect(
      buildLiveActivitySteps([
        {
          kind: 'tool',
          id: 'batch-call',
          name: 'edit_file',
          input,
          approval,
        },
      ])[0],
    ).toMatchObject({ label: 'Change 3 files', status: 'approval_required' });
  });

  it('builds command approval details and appends correlated output', () => {
    const input = {
      command: 'go',
      args: ['test', './...'],
      workingDirectory: '../..',
      timeoutSeconds: 180,
    };
    const approval = buildLiveToolApproval({
      id: 'approval-command',
      toolCallId: 'command-call',
      toolName: 'run_command',
      input,
      payload: {
        kind: 'run_command',
        ...input,
        executable: '/usr/local/go/bin/go',
        workingDirectory: '/home/user/repository',
      },
    });
    const activity = appendLiveCommandOutput(
      [
        {
          kind: 'tool',
          id: 'command-call',
          name: 'run_command',
          input,
          approval: { ...approval, status: 'approved' },
        },
      ],
      { toolCallId: 'command-call', stream: 'stdout', text: 'ok\n' },
    );
    const steps = buildLiveActivitySteps(activity);

    expect(approval).toMatchObject({
      kind: 'command_run',
      target: 'go test ./...',
      command: {
        commandLine: 'go test ./...',
        workingDirectory: '/home/user/repository',
        timeoutSeconds: 180,
      },
    });
    expect(steps[0]).toMatchObject({
      label: 'Run go test ./...',
      icon: 'terminal',
      status: 'running',
      command: { stdout: 'ok\n', stderr: '' },
    });
  });

  it('renders persisted command output as structured command details', () => {
    const timeline = buildChatTimeline([
      message('user-command', 'Run tests', 'user'),
      {
        id: 'command',
        invocationId: 'invocation-1',
        kind: 'tool_call',
        toolName: 'run_command',
        toolCallId: 'command-call',
        toolInput: { command: 'go', args: ['test', './...'] },
        createdAt: '2026-07-21T08:00:00Z',
      },
      toolResult('command-result', 'command-call', 'run_command', {
        state: 'completed',
        command: 'go',
        args: ['test', './...'],
        workingDirectory: '/home/user/repository',
        timeoutSeconds: 120,
        exitCode: 0,
        stdout: 'ok\n',
        stderr: '',
        durationMs: 25,
      }),
      message('answer-command', 'Tests pass.', 'assistant'),
    ]);
    const activity = timeline.find((item) => item.kind === 'activity');

    expect(activity?.kind).toBe('activity');
    if (activity?.kind !== 'activity') {
      throw new Error('expected activity entry');
    }
    expect(activity.steps[0].command).toMatchObject({
      commandLine: 'go test ./...',
      exitCode: 0,
      stdout: 'ok\n',
      workingDirectory: '/home/user/repository',
    });
  });

  it('renders ACP thoughts separately from agent tool activity', () => {
    const transcript: TranscriptItem[] = [
      message('acp-user', 'Inspect the project', 'user'),
      {
        id: 'acp-thought',
        invocationId: 'acp-run',
        kind: 'thought',
        role: 'assistant',
        text: 'I will run the tests.',
        provider: 'acp',
        model: 'Codex ACP',
        createdAt: '2026-07-21T08:00:00Z',
      },
      {
        id: 'acp-command',
        invocationId: 'acp-run',
        kind: 'tool_call',
        toolName: 'run_command',
        toolCallId: 'acp-command-call',
        toolInput: {
          title: 'Run the Go test suite',
          command: 'go',
          args: ['test', './...'],
          workingDirectory: '/workspace',
        },
        provider: 'acp',
        model: 'Codex ACP',
        createdAt: '2026-07-21T08:00:01Z',
      },
      {
        id: 'acp-command-result',
        invocationId: 'acp-run',
        kind: 'tool_result',
        toolName: 'run_command',
        toolCallId: 'acp-command-call',
        toolOutput: { state: 'completed', exitCode: 0, stdout: 'ok\n' },
        provider: 'acp',
        model: 'Codex ACP',
        createdAt: '2026-07-21T08:00:02Z',
      },
      {
        ...message('acp-answer', 'The tests pass.', 'assistant'),
        invocationId: 'acp-run',
        provider: 'acp',
        model: 'Codex ACP',
      },
    ];
    transcript[0] = { ...transcript[0], invocationId: 'acp-run' };

    const timeline = buildChatTimeline(transcript);
    expect(timeline.map((item) => item.kind)).toEqual(['message', 'note', 'activity', 'message']);
    expect(timeline[1]).toMatchObject({ kind: 'note', text: 'I will run the tests.' });
    expect(timeline[2]).toMatchObject({
      kind: 'activity',
      steps: [
        {
          label: 'Run the Go test suite',
          icon: 'terminal',
          status: 'complete',
          command: { stdout: 'ok\n', workingDirectory: '/workspace' },
        },
      ],
    });
  });

  it('preserves complete ACP command lines without parsing shell syntax', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'acp-command',
        name: 'run_command',
        input: {
          command: '/bin/bash -lc "go test ./..."',
          title: 'go test ./...',
          workingDirectory: '/workspace',
        },
        output: { state: 'completed', exitCode: 0 },
      },
      {
        kind: 'tool',
        id: 'quoted-acp-command',
        name: 'run_command',
        input: {
          command: '"jj status --no-pager"',
          title: 'jj status --no-pager',
          workingDirectory: '/workspace',
        },
        output: { state: 'completed', exitCode: 0 },
      },
      {
        kind: 'tool',
        id: 'compound-acp-command',
        name: 'run_command',
        input: {
          command:
            'GOCACHE=/tmp/materialmind-release-gocache npm run build:release && go test ./...',
          title: 'Build and test the release',
          workingDirectory: '/workspace',
        },
        output: { state: 'completed', exitCode: 0 },
      },
    ]);

    expect(steps[0].command).toMatchObject({
      commandLine: '/bin/bash -lc "go test ./..."',
    });
    expect(steps[1].command).toMatchObject({
      commandLine: '"jj status --no-pager"',
    });
    expect(steps[2].command).toMatchObject({
      commandLine:
        'GOCACHE=/tmp/materialmind-release-gocache npm run build:release && go test ./...',
    });
  });

  it('drops synthetic ACP command-title errors while retaining real failures', () => {
    const command = 'node -e "process.exit(1)"';
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'failed-command-title',
        name: 'run_command',
        input: { command, kind: 'execute', title: command },
        output: { state: 'failed', kind: 'execute', title: command, error: command },
      },
      {
        kind: 'tool',
        id: 'failed-command-detail',
        name: 'run_command',
        input: { command, kind: 'execute', title: command },
        output: {
          state: 'failed',
          kind: 'execute',
          title: command,
          error: 'process could not be started',
        },
      },
    ]);

    expect(steps[0].command?.error).toBeUndefined();
    expect(steps[1].command?.error).toBe('process could not be started');
  });

  it('renders structured command arguments as one Bash-safe command line', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'structured-command',
        name: 'run_command',
        input: {
          command: 'printf',
          args: ['%s\\n', 'literal value', "it's"],
          workingDirectory: '/workspace',
        },
        output: { state: 'completed', exitCode: 0 },
      },
    ]);

    expect(steps[0].command).toMatchObject({
      commandLine: `printf '%s\\n' 'literal value' 'it'"'"'s'`,
    });
  });

  it('normalizes ACP-provided MCP arguments, results, and failures', () => {
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'acp-mcp-call',
        name: 'run_command',
        input: {
          server: 'Atlassian',
          tool: 'createJiraIssue',
          arguments: { projectKey: 'TEST', summary: 'Broken workflow' },
          title: 'mcp.Atlassian.createJiraIssue',
        },
        output: {
          state: 'failed',
          result: {
            content: [
              {
                type: 'text',
                text: JSON.stringify({
                  error: true,
                  message: 'Project TEST does not allow this issue type.',
                }),
              },
            ],
            structuredContent: { code: 'INVALID_ISSUE_TYPE' },
          },
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      label: 'Run createJiraIssue',
      icon: 'extension',
      status: 'failed',
      command: undefined,
      mcp: {
        serverId: 'Atlassian',
        serverName: 'Atlassian',
        toolName: 'createJiraIssue',
        content: [
          {
            type: 'text',
            text: '{"error":true,"message":"Project TEST does not allow this issue type."}',
          },
        ],
        structuredContent: { code: 'INVALID_ISSUE_TYPE' },
        isError: true,
        error: 'Project TEST does not allow this issue type.',
      },
    });
  });

  it('reads the latest structured plan for the selected invocation', () => {
    const transcript: TranscriptItem[] = [
      {
        id: 'older-plan',
        invocationId: 'older-run',
        kind: 'plan',
        toolOutput: {
          entries: [{ content: 'Old work', priority: 'low', status: 'completed' }],
        },
        createdAt: '2026-07-21T08:00:00Z',
      },
      {
        id: 'current-plan',
        invocationId: 'current-run',
        kind: 'plan',
        toolOutput: {
          entries: [
            { content: 'Inspect the code', priority: 'high', status: 'completed' },
            { content: 'Implement the change', priority: 'medium', status: 'in_progress' },
            { content: 'Verify the result', priority: 'medium', status: 'pending' },
          ],
        },
        createdAt: '2026-07-21T08:00:01Z',
      },
    ];

    expect(latestAgentPlan(transcript, 'current-run')).toEqual({
      id: 'current-plan',
      entries: [
        { content: 'Inspect the code', priority: 'high', status: 'completed' },
        { content: 'Implement the change', priority: 'medium', status: 'in_progress' },
        { content: 'Verify the result', priority: 'medium', status: 'pending' },
      ],
    });
    expect(buildChatTimeline(transcript)).toEqual([]);
  });

  it('supports plans persisted by older ACP runs', () => {
    const transcript: TranscriptItem[] = [
      {
        id: 'legacy-plan',
        invocationId: 'legacy-run',
        kind: 'thought',
        role: 'assistant',
        text: 'Plan\n- [x] Inspect the code\n- [>] Implement the change\n- [ ] Run tests',
        createdAt: '2026-07-21T08:00:00Z',
      },
    ];

    expect(latestAgentPlan(transcript, 'legacy-run')).toEqual({
      id: 'legacy-plan',
      entries: [
        { content: 'Inspect the code', priority: 'medium', status: 'completed' },
        { content: 'Implement the change', priority: 'medium', status: 'in_progress' },
        { content: 'Run tests', priority: 'medium', status: 'pending' },
      ],
    });
  });

  it('maps ACP file edit output to diff previews', () => {
    const diff = '--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n';
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'acp-edit',
        name: 'edit_file',
        input: {
          title: 'Update main.go',
          kind: 'edit',
          files: [{ operation: 'update', path: 'main.go', diff }],
        },
        output: {
          state: 'completed',
          title: 'Update main.go',
          kind: 'edit',
          files: [{ operation: 'update', path: 'main.go', diff }],
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      label: 'Update main.go',
      status: 'complete',
      fileChanges: [{ operation: 'update', path: 'main.go', diff }],
    });
  });

  it('splits a completed multi-file edit result into diff previews', () => {
    const firstDiff =
      '--- a//home/user/.agents/skills/review/SKILL.md\n' +
      '+++ b//home/user/.agents/skills/review/SKILL.md\n' +
      '@@ -1 +1 @@\n-old review\n+new review\n';
    const secondDiff =
      '--- /dev/null\n' + '+++ b/references/checks.md\n' + '@@ -0,0 +1 @@\n+Check the result.\n';
    const steps = buildLiveActivitySteps([
      {
        kind: 'tool',
        id: 'multi-edit',
        name: 'edit_file',
        input: {
          changes: [
            { operation: 'update', path: '/home/user/.agents/skills/review/SKILL.md' },
            { operation: 'create', path: 'references/checks.md' },
          ],
        },
        output: {
          state: 'applied',
          paths: ['/home/user/.agents/skills/review/SKILL.md', 'references/checks.md'],
          diff: `${firstDiff}\n${secondDiff}`,
        },
      },
    ]);

    expect(steps[0]).toMatchObject({
      label: 'Change 2 files',
      status: 'complete',
      fileChanges: [
        {
          operation: 'update',
          path: '/home/user/.agents/skills/review/SKILL.md',
          diff: firstDiff,
        },
        {
          operation: 'create',
          path: 'references/checks.md',
          diff: secondDiff,
        },
      ],
    });
  });
});

function message(id: string, text: string, role: 'user' | 'assistant'): TranscriptItem {
  return {
    id,
    invocationId: 'invocation-1',
    kind: 'message',
    role,
    text,
    createdAt: '2026-07-21T08:00:00Z',
  };
}

function toolCall(id: string, toolCallId: string, toolName: string, path: string): TranscriptItem {
  return {
    id,
    invocationId: 'invocation-1',
    kind: 'tool_call',
    toolName,
    toolCallId,
    toolInput: { path },
    createdAt: '2026-07-21T08:00:00Z',
  };
}

function toolResult(
  id: string,
  toolCallId: string,
  toolName: string,
  toolOutput: Record<string, unknown>,
): TranscriptItem {
  return {
    id,
    invocationId: 'invocation-1',
    kind: 'tool_result',
    toolName,
    toolCallId,
    toolOutput,
    createdAt: '2026-07-21T08:00:00Z',
  };
}
