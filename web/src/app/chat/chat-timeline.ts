import {
  AgentPlan,
  AgentPlanEntry,
  AgentRun,
  MCPElicitationResolution,
  StreamACPElicitationCompletion,
  StreamContextCompaction,
  StreamCommandOutput,
  StreamMCPCallFinished,
  StreamMCPCallStarted,
  StreamMCPLog,
  StreamMCPProgress,
  StreamMCPElicitationRequest,
  StreamToolApproval,
  StreamUserInputRequest,
  TranscriptItem,
  UserInputAnswer,
  UserQuestion,
} from '../core/models';
import {
  FileChangeOperation,
  FileChangePreview,
  ToolApprovalState,
} from '../shared/tool-approval/tool-approval.models';
import { UserInputState } from '../shared/user-input/user-input.models';
import { MCPElicitationState } from '../shared/mcp-elicitation/mcp-elicitation.models';

export type ActivityStatus =
  | 'complete'
  | 'running'
  | 'queued'
  | 'approval_required'
  | 'input_required'
  | 'denied'
  | 'cancelled'
  | 'failed'
  | 'incomplete';

export interface CommandApprovalDetails {
  commandLine: string;
  workingDirectory: string;
  timeoutSeconds: number;
}

export interface CommandActivityDetails {
  commandLine: string;
  workingDirectory: string;
  timeoutSeconds: number;
  stdout: string;
  stderr: string;
  state?: string;
  exitCode?: number;
  durationMs?: number;
  timedOut: boolean;
  stdoutTruncated: boolean;
  stderrTruncated: boolean;
  error?: string;
}

export interface FetchActivityDetails {
  state?: string;
  requestedUrl: string;
  finalUrl?: string;
  httpStatus?: number;
  contentType?: string;
  truncated: boolean;
  reason?: string;
}

export interface MCPContentItem {
  type: string;
  [key: string]: unknown;
}

export interface MCPLogEntry {
  level: string;
  logger?: string;
  data: unknown;
}

export interface MCPActivityDetails {
  serverId: string;
  serverName: string;
  toolName: string;
  toolTitle?: string;
  uiResourceUri?: string;
  cancelable: boolean;
  cancelling: boolean;
  message?: string;
  progress?: number;
  total?: number;
  logs: readonly MCPLogEntry[];
  content: readonly MCPContentItem[];
  structuredContent?: unknown;
  isError: boolean;
  error?: string;
}

export interface LiveCommandOutput {
  stdout: string;
  stderr: string;
}

export interface LiveToolApproval extends ToolApprovalState {
  kind:
    | 'fetch_url'
    | 'file_edit'
    | 'filesystem_access'
    | 'skill_load'
    | 'session_notes'
    | 'command_run'
    | 'mcp_tool'
    | 'generic';
  prompt: string;
  icon: string;
  targetLabel: string;
  target: string;
  diff?: string;
  files?: readonly FileChangePreview[];
  command?: CommandApprovalDetails;
  hint?: string;
}

export interface AgentActivityStep {
  id: string;
  toolName?: string;
  label: string;
  icon: string;
  status: ActivityStatus;
  input?: Record<string, unknown>;
  output?: Record<string, unknown>;
  approval?: LiveToolApproval;
  userInput?: UserInputState;
  mcpElicitation?: MCPElicitationState;
  command?: CommandActivityDetails;
  fetch?: FetchActivityDetails;
  sessionNotes?: SessionNotesActivityDetails;
  mcp?: MCPActivityDetails;
  fileChanges?: readonly FileChangePreview[];
}

export interface SessionNotesActivityDetails {
  operation: 'read' | 'update';
  state?: string;
  content: string;
  revision?: number;
  expectedRevision?: number;
  bytes?: number;
  updatedAt?: string;
  reason?: string;
}

export interface LiveToolActivity {
  kind: 'tool';
  id: string;
  name: string;
  input: Record<string, unknown>;
  output?: Record<string, unknown>;
  approval?: LiveToolApproval;
  userInput?: UserInputState;
  mcpElicitation?: MCPElicitationState;
  commandOutput?: LiveCommandOutput;
  mcp?: MCPActivityDetails;
}

export interface LiveNoteActivity {
  kind: 'note';
  id: string;
  text: string;
}

export interface LiveSubagentActivity {
  kind: 'subagent';
  id: string;
  name: string;
  label: string;
  task: string;
  status: ActivityStatus;
  output?: Record<string, unknown>;
  activities: LiveActivity[];
}

export type LiveActivity = LiveToolActivity | LiveNoteActivity | LiveSubagentActivity;

const maxLiveCommandOutputCharacters = 512 * 1024;

export type AgentDetailTimelineItem =
  | {
      kind: 'message';
      id: string;
      message: TranscriptItem;
    }
  | {
      kind: 'note';
      id: string;
      text: string;
    }
  | {
      kind: 'activity';
      id: string;
      steps: AgentActivityStep[];
      running?: boolean;
    };

export interface AgentDelegation {
  id: string;
  name: string;
  label: string;
  task: string;
  status: ActivityStatus;
  result?: string;
  timeline: AgentDetailTimelineItem[];
}

export function effectiveDelegationStatus(
  delegation: Pick<AgentDelegation, 'status' | 'timeline'>,
): ActivityStatus {
  const nestedStatuses = delegation.timeline.flatMap((entry) =>
    entry.kind === 'activity' ? entry.steps.map((step) => step.status) : [],
  );
  for (const status of ['approval_required', 'input_required'] as const) {
    if (nestedStatuses.includes(status)) {
      return status;
    }
  }
  if (isTerminalActivityStatus(delegation.status)) {
    return delegation.status;
  }
  for (const status of ['running', 'queued'] as const) {
    if (nestedStatuses.includes(status)) {
      return status;
    }
  }
  return delegation.status;
}

function isTerminalActivityStatus(status: ActivityStatus): boolean {
  return (
    status === 'complete' ||
    status === 'denied' ||
    status === 'cancelled' ||
    status === 'failed' ||
    status === 'incomplete'
  );
}

export type ChatTimelineItem =
  | AgentDetailTimelineItem
  | {
      kind: 'subagent';
      id: string;
      delegation: AgentDelegation;
    };

export function buildChatTimeline(
  transcript: readonly TranscriptItem[],
  runs: readonly Pick<AgentRun, 'invocationId' | 'status'>[] = [],
): ChatTimelineItem[] {
  const batches = groupByInvocation(transcript);
  const runStatuses = new Map(
    runs
      .filter((run) => run.invocationId)
      .map((run) => [run.invocationId as string, run.status] as const),
  );

  return batches.flatMap((batch) =>
    buildInvocationTimeline(batch, runStatuses.get(batch[0]?.invocationId ?? '')),
  );
}

export function transcriptBeforeActiveInvocation(
  transcript: readonly TranscriptItem[],
  activeInvocationId?: string,
): readonly TranscriptItem[] {
  if (!activeInvocationId) {
    return transcript;
  }
  return transcript.filter((item) => item.invocationId !== activeInvocationId);
}

export function buildLiveActivitySteps(items: readonly LiveActivity[]): AgentActivityStep[] {
  return items.flatMap((item) => {
    if (item.kind !== 'tool') {
      return [];
    }
    const userInputPending =
      item.userInput?.status === 'pending' || item.userInput?.status === 'submitting';
    const elicitationPending =
      item.mcpElicitation?.status === 'pending' || item.mcpElicitation?.status === 'submitting';
    const approvalPending =
      item.approval?.status === 'pending' || item.approval?.status === 'submitting';
    const executionStarted =
      item.approval?.status === 'executing' || item.commandOutput !== undefined;
    const status: ActivityStatus =
      userInputPending || elicitationPending
        ? 'input_required'
        : approvalPending
          ? 'approval_required'
          : item.approval?.status === 'denied'
            ? 'denied'
            : item.output
              ? toolResultStatus(item.output)
              : item.approval?.status === 'approved' && !executionStarted
                ? 'queued'
                : 'running';
    return toolStep(item, status);
  });
}

export function buildLiveMCPElicitation(request: StreamMCPElicitationRequest): MCPElicitationState {
  return { ...request, status: 'pending' };
}

export function applyLiveMCPElicitationRequest(
  items: readonly LiveActivity[],
  request: StreamMCPElicitationRequest,
): LiveActivity[] {
  const elicitation = buildLiveMCPElicitation(request);
  const update = updateLiveToolActivity(items, request.toolCallId, (item) => ({
    ...item,
    mcpElicitation: elicitation,
  }));
  if (update.matched) {
    return update.items;
  }
  return [
    ...update.items,
    {
      kind: 'tool',
      id: request.toolCallId,
      name: request.source === 'acp' ? 'acp_elicitation' : 'mcp_elicitation',
      input: {},
      mcpElicitation: elicitation,
      ...(request.source === 'acp'
        ? {}
        : {
            mcp: mergeMCPDetails(undefined, {
              serverId: request.serverId,
              serverName: request.serverName,
              toolName: 'elicitation',
              toolTitle: 'Request user input',
              cancelable: false,
            }),
          }),
    },
  ];
}

export function applyLiveMCPElicitationResolution(
  items: readonly LiveActivity[],
  resolution: MCPElicitationResolution,
): LiveActivity[] {
  return updateLiveToolActivity(items, resolution.toolCallId, (item) =>
    item.mcpElicitation?.id === resolution.id
      ? {
          ...item,
          mcpElicitation: { ...item.mcpElicitation, status: 'resolved', resolution },
        }
      : item,
  ).items;
}

export function applyLiveACPElicitationCompletion(
  items: readonly LiveActivity[],
  completion: StreamACPElicitationCompletion,
): LiveActivity[] {
  return items.map((item): LiveActivity => {
    if (item.kind === 'subagent') {
      return {
        ...item,
        activities: applyLiveACPElicitationCompletion(item.activities, completion),
      };
    }
    if (item.kind !== 'tool' || item.mcpElicitation?.id !== completion.id) {
      return item;
    }
    return {
      ...item,
      mcpElicitation: { ...item.mcpElicitation, externalCompleted: true },
    };
  });
}

export function applyLiveContextCompaction(
  items: readonly LiveActivity[],
  update: StreamContextCompaction,
): LiveActivity[] {
  const input: Record<string, unknown> = {
    title: 'Compact context',
    estimatedTokensBefore: update.estimatedTokensBefore,
    maxContextTokens: update.maxContextTokens,
    summarizedContents: update.summarizedContents,
  };
  const output: Record<string, unknown> | undefined =
    update.status === 'running'
      ? undefined
      : {
          state: update.status,
          ...(update.estimatedTokensAfter !== undefined
            ? { estimatedTokensAfter: update.estimatedTokensAfter }
            : {}),
          ...(update.error ? { error: update.error } : {}),
        };
  let matched = false;
  const updated = items.map((item): LiveActivity => {
    if (item.kind !== 'tool' || item.id !== update.id) {
      return item;
    }
    matched = true;
    return { ...item, name: 'context_compaction', input, output };
  });
  return matched
    ? updated
    : [...updated, { kind: 'tool', id: update.id, name: 'context_compaction', input, output }];
}

export type LiveChatTimelineItem =
  | {
      kind: 'note';
      id: string;
      text: string;
    }
  | {
      kind: 'activity';
      id: string;
      steps: AgentActivityStep[];
      running: boolean;
    }
  | {
      kind: 'subagent';
      id: string;
      delegation: AgentDelegation;
    };

type LiveAgentDetailTimelineItem = Exclude<LiveChatTimelineItem, { kind: 'subagent' }>;

export function buildLiveChatTimeline(items: readonly LiveActivity[]): LiveChatTimelineItem[] {
  const timeline: LiveChatTimelineItem[] = [];
  let tools: LiveToolActivity[] = [];
  const flushTools = () => {
    if (tools.length === 0) {
      return;
    }
    const steps = buildLiveActivitySteps(tools);
    timeline.push({
      kind: 'activity',
      id: `live-activity:${tools[0].id}`,
      steps,
      running: steps.some(
        (step) =>
          step.status === 'running' ||
          step.status === 'queued' ||
          step.status === 'approval_required' ||
          step.status === 'input_required',
      ),
    });
    tools = [];
  };

  for (const item of items) {
    if (item.kind === 'subagent') {
      flushTools();
      const detailTimeline = buildLiveDetailTimeline(item.activities);
      const delegation: AgentDelegation = {
        id: item.id,
        name: item.name,
        label: item.label,
        task: item.task,
        status: item.status,
        result: subagentResultText(item.output),
        timeline: detailTimeline,
      };
      delegation.status = effectiveDelegationStatus(delegation);
      timeline.push({
        kind: 'subagent',
        id: `live-subagent:${item.id}`,
        delegation,
      });
      continue;
    }
    if (item.kind === 'note') {
      flushTools();
      timeline.push({ kind: 'note', id: item.id, text: item.text });
      continue;
    }
    tools.push(item);
  }
  flushTools();
  return timeline;
}

function buildLiveDetailTimeline(items: readonly LiveActivity[]): AgentDetailTimelineItem[] {
  return buildLiveChatTimeline(items).filter(
    (item): item is LiveAgentDetailTimelineItem => item.kind !== 'subagent',
  );
}

export function latestAgentPlan(
  transcript: readonly TranscriptItem[],
  invocationId?: string,
): AgentPlan | null {
  for (let index = transcript.length - 1; index >= 0; index--) {
    const item = transcript[index];
    if (invocationId && item.invocationId !== invocationId) {
      continue;
    }
    const plan = agentPlanFromTranscript(item);
    if (plan) {
      return plan.entries.length > 0 ? plan : null;
    }
  }
  return null;
}

export function buildLiveUserInput(request: StreamUserInputRequest): UserInputState {
  const questions = Array.isArray(request.questions) ? request.questions : [];
  return {
    ...request,
    questions: questions.map((question) => ({
      ...question,
      options: Array.isArray(question.options) ? question.options : [],
    })),
    status: 'pending',
  };
}

export function appendLiveCommandOutput(
  items: readonly LiveActivity[],
  output: StreamCommandOutput,
): LiveActivity[] {
  const update = updateLiveToolActivity(items, output.toolCallId, (item) => {
    const current = item.commandOutput ?? { stdout: '', stderr: '' };
    return {
      ...item,
      commandOutput: {
        ...current,
        [output.stream]: appendCommandOutput(current[output.stream], output.text),
      },
    };
  });
  if (update.matched) {
    return update.items;
  }
  return [
    ...update.items,
    {
      kind: 'tool',
      id: output.toolCallId,
      name: 'run_command',
      input: {},
      commandOutput: {
        stdout: output.stream === 'stdout' ? appendCommandOutput('', output.text) : '',
        stderr: output.stream === 'stderr' ? appendCommandOutput('', output.text) : '',
      },
    },
  ];
}

export function applyLiveMCPCallStarted(
  items: readonly LiveActivity[],
  event: StreamMCPCallStarted,
): LiveActivity[] {
  const update = updateLiveToolActivity(items, event.toolCallId, (item) => ({
    ...item,
    mcp: mergeMCPDetails(item.mcp, event),
  }));
  if (update.matched) {
    return update.items;
  }
  return [
    ...update.items,
    {
      kind: 'tool',
      id: event.toolCallId,
      name: event.toolName,
      input: {},
      mcp: mergeMCPDetails(undefined, event),
    },
  ];
}

export function applyLiveMCPProgress(
  items: readonly LiveActivity[],
  event: StreamMCPProgress,
): LiveActivity[] {
  return applyLiveMCPCallStarted(
    updateLiveToolActivity(items, event.toolCallId, (item) => ({
      ...item,
      mcp: {
        ...mergeMCPDetails(item.mcp, event),
        message: event.message,
        progress: event.progress,
        total: event.total,
      },
    })).items,
    event,
  );
}

export function applyLiveMCPCallFinished(
  items: readonly LiveActivity[],
  event: StreamMCPCallFinished,
): LiveActivity[] {
  const started = applyLiveMCPCallStarted(items, event);
  return updateLiveToolActivity(started, event.toolCallId, (item) => ({
    ...item,
    output: event.output,
    mcp: {
      ...mergeMCPDetails(item.mcp, event),
      cancelable: false,
      cancelling: false,
    },
  })).items;
}

export function applyLiveMCPLog(
  items: readonly LiveActivity[],
  event: StreamMCPLog,
): { items: LiveActivity[]; matched: boolean } {
  if (!event.toolCallId) {
    return { items: [...items], matched: false };
  }
  return updateLiveToolActivity(items, event.toolCallId, (item) => {
    const details = mergeMCPDetails(item.mcp, {
      ...event,
      toolName: event.toolName ?? item.mcp?.toolName ?? item.name,
      cancelable: item.mcp?.cancelable ?? true,
    });
    return {
      ...item,
      mcp: {
        ...details,
        logs: [
          ...details.logs,
          { level: event.level, logger: event.logger, data: event.data },
        ].slice(-100),
      },
    };
  });
}

export function updateLiveToolActivity(
  items: readonly LiveActivity[],
  toolCallId: string,
  update: (item: LiveToolActivity) => LiveToolActivity,
): { items: LiveActivity[]; matched: boolean } {
  let matched = false;
  const updated = items.map((item): LiveActivity => {
    if (item.kind === 'subagent') {
      const nested = updateLiveToolActivity(item.activities, toolCallId, update);
      matched ||= nested.matched;
      return nested.matched ? { ...item, activities: nested.items } : item;
    }
    if (item.kind !== 'tool' || item.id !== toolCallId) {
      return item;
    }
    matched = true;
    return update(item);
  });
  return { items: updated, matched };
}

export function buildLiveToolApproval(request: StreamToolApproval): LiveToolApproval {
  const payload = request.payload ?? {};
  const options = request.options?.length ? request.options : undefined;
  const kind = stringValue(payload['kind']);
  if (kind === 'file_patch' || kind === 'file_edit' || request.toolName === 'edit_file') {
    const payloadFiles = fileChangePreviews(payload['files']);
    const legacyPath = stringValue(payload['path']) || stringValue(request.input['path']);
    const legacyDiff = stringValue(payload['diff']);
    const files =
      payloadFiles.length > 0
        ? payloadFiles
        : legacyPath
          ? [{ operation: 'update' as const, path: legacyPath, diff: legacyDiff }]
          : [];
    const multiple = files.length > 1;
    return {
      id: request.id,
      kind: 'file_edit',
      prompt: multiple ? 'Allow these file changes?' : 'Allow this file change?',
      icon: multiple ? 'difference' : 'edit_note',
      targetLabel: multiple ? 'Files' : 'File',
      target: multiple ? `${files.length} files` : files[0]?.path || 'Unknown file',
      diff: legacyDiff,
      files,
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (kind === 'fetch_url' || request.toolName === 'fetch_url') {
    return {
      id: request.id,
      kind: 'fetch_url',
      prompt: 'Allow this fetch request?',
      icon: 'language',
      targetLabel: 'URL',
      target:
        stringValue(payload['url']) ||
        request.url ||
        stringValue(request.input['url']) ||
        'Unknown URL',
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (
    kind === 'filesystem_access' ||
    request.toolName === 'read_file' ||
    request.toolName === 'list_directory' ||
    request.toolName === 'grep'
  ) {
    const operation = stringValue(payload['operation']);
    const isDirectory = operation === 'list' || request.toolName === 'list_directory';
    const isSearch = operation === 'search' || request.toolName === 'grep';
    return {
      id: request.id,
      kind: 'filesystem_access',
      prompt: isDirectory
        ? 'Allow this directory listing?'
        : isSearch
          ? 'Allow this file search?'
          : 'Allow this file read?',
      icon: isDirectory ? 'folder_open' : isSearch ? 'search' : 'description',
      targetLabel: 'Path',
      target:
        stringValue(payload['absolutePath']) ||
        stringValue(payload['path']) ||
        stringValue(request.input['path']) ||
        'Unknown path',
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (kind === 'skill_load' || request.toolName === 'load_skill') {
    const name = stringValue(payload['name']) || stringValue(request.input['name']);
    const resource = stringValue(payload['resource']) || stringValue(request.input['resource']);
    const loadsResource = resource !== '' && resource !== 'SKILL.md';
    return {
      id: request.id,
      kind: 'skill_load',
      prompt: loadsResource ? 'Allow this skill resource to be loaded?' : 'Allow this skill?',
      icon: 'auto_stories',
      targetLabel: loadsResource ? 'Resource' : 'Skill',
      target: loadsResource ? `${name || 'Unknown skill'} / ${resource}` : name || 'Unknown skill',
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (
    kind === 'session_notes' ||
    request.toolName === 'read_session_notes' ||
    request.toolName === 'update_session_notes'
  ) {
    const operation =
      stringValue(payload['operation']) ||
      (request.toolName === 'update_session_notes' ? 'update' : 'read');
    const updating = operation === 'update';
    return {
      id: request.id,
      kind: 'session_notes',
      prompt: updating
        ? 'Allow the agent to update the session notes?'
        : 'Allow the agent to read the session notes?',
      icon: updating ? 'edit_note' : 'note_alt',
      targetLabel: 'Scope',
      target: 'Current session',
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (kind === 'run_command' || request.toolName === 'run_command') {
    const command = stringValue(payload['command']) || stringValue(request.input['command']);
    const payloadArgs = stringValues(payload['args']);
    const args = payloadArgs.length ? payloadArgs : stringValues(request.input['args']);
    const commandLine = formatCommandLine(command, args);
    return {
      id: request.id,
      kind: 'command_run',
      prompt: 'Allow this command to run?',
      icon: 'terminal',
      targetLabel: 'Command',
      target: commandLine || 'Unknown command',
      command: {
        commandLine,
        workingDirectory: stringValue(payload['workingDirectory']) || '.',
        timeoutSeconds: numberValue(payload['timeoutSeconds']),
      },
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  if (kind === 'mcp_tool') {
    const serverName = stringValue(payload['serverName']) || 'MCP server';
    const toolName =
      stringValue(payload['toolName']) ||
      stringValue(payload['namespacedToolName']) ||
      request.toolName;
    return {
      id: request.id,
      kind: 'mcp_tool',
      prompt: request.hint || `Allow ${serverName} to run ${toolName}?`,
      icon: 'extension',
      targetLabel: 'Server and tool',
      target: `${serverName} / ${toolName}`,
      hint: request.hint,
      ...(options ? { options } : {}),
      status: 'pending',
    };
  }
  return {
    id: request.id,
    kind: 'generic',
    prompt: request.hint || 'Allow this tool action?',
    icon: 'approval',
    targetLabel: 'Tool',
    target: request.toolName,
    hint: request.hint,
    ...(options ? { options } : {}),
    status: 'pending',
  };
}

function groupByInvocation(transcript: readonly TranscriptItem[]): TranscriptItem[][] {
  const batches: TranscriptItem[][] = [];
  let currentKey: string | null = null;

  for (const item of transcript) {
    const key = item.invocationId ?? `item:${item.id}`;
    if (key !== currentKey) {
      batches.push([]);
      currentKey = key;
    }
    batches.at(-1)?.push(item);
  }

  return batches;
}

function buildInvocationTimeline(
  items: readonly TranscriptItem[],
  runStatus: AgentRun['status'] | undefined,
  nested = false,
): ChatTimelineItem[] {
  const visibleItems = nested ? items : items.filter((item) => !item.delegationId);
  const finalAssistantId =
    [...visibleItems].reverse().find((item) => item.kind === 'message' && item.role === 'assistant')
      ?.id ?? null;
  const results = new Map(
    items
      .filter(
        (item) => item.kind === 'tool_result' && item.toolCallId && (nested || !item.delegationId),
      )
      .map((item) => [item.toolCallId as string, item]),
  );
  const subagentResults = new Map(
    items
      .filter((item) => item.kind === 'subagent_result' && item.toolCallId)
      .map((item) => [item.toolCallId as string, item]),
  );
  const matchedResultIds = new Set<string>();
  const timeline: ChatTimelineItem[] = [];
  let steps: AgentActivityStep[] = [];
  const flushSteps = () => {
    if (steps.length === 0) {
      return;
    }
    timeline.push({
      kind: 'activity',
      id: `activity:${items[0]?.invocationId ?? items[0]?.id}:${steps[0].id}`,
      steps,
    });
    steps = [];
  };

  for (const item of items) {
    if (!nested && item.delegationId) {
      continue;
    }

    if (agentPlanFromTranscript(item)) {
      flushSteps();
      continue;
    }

    if (item.kind === 'context_compaction') {
      flushSteps();
      steps.push(
        toolStep(
          {
            id: item.id,
            name: 'context_compaction',
            input: item.toolInput ?? {},
            output: item.toolOutput,
          },
          toolResultStatus(item.toolOutput),
        ),
      );
      flushSteps();
      continue;
    }

    if (item.kind === 'message' && item.role === 'user') {
      flushSteps();
      timeline.push({ kind: 'message', id: item.id, message: item });
      continue;
    }

    if (item.kind === 'subagent_call') {
      flushSteps();
      const id = item.toolCallId || item.id;
      const result = subagentResults.get(id);
      const childItems = items.filter((candidate) => candidate.delegationId === id);
      timeline.push({
        kind: 'subagent',
        id: `subagent:${id}`,
        delegation: {
          id,
          name: item.agentName || item.toolName || 'subagent',
          label: item.agentLabel || humanize(item.toolName || 'Sub-agent'),
          task: stringValue(item.toolInput?.['request']),
          status: result ? toolResultStatus(result.toolOutput) : missingResultStatus(runStatus),
          result: subagentResultText(result?.toolOutput),
          timeline: buildInvocationTimeline(childItems, runStatus, true).filter(
            (entry): entry is AgentDetailTimelineItem => entry.kind !== 'subagent',
          ),
        },
      });
      continue;
    }

    if (item.kind === 'subagent_result') {
      continue;
    }

    if (item.kind === 'message' && item.id === finalAssistantId) {
      flushSteps();
      timeline.push({ kind: 'message', id: item.id, message: item });
      continue;
    }

    if (item.kind === 'tool_call') {
      const result = item.toolCallId ? results.get(item.toolCallId) : undefined;
      if (result) {
        matchedResultIds.add(result.id);
      }
      steps.push(
        toolStep(
          {
            id: item.id,
            name: item.toolName ?? 'Tool',
            input: item.toolInput ?? {},
            output: result?.toolOutput,
          },
          result ? toolResultStatus(result.toolOutput) : missingResultStatus(runStatus),
        ),
      );
      continue;
    }

    if (item.kind === 'tool_result' && !matchedResultIds.has(item.id)) {
      steps.push(
        toolStep(
          {
            id: item.id,
            name: item.toolName ?? 'Tool',
            input: {},
            output: item.toolOutput,
          },
          toolResultStatus(item.toolOutput),
        ),
      );
      continue;
    }

    if (
      item.kind === 'thought' ||
      (item.kind === 'message' && item.role === 'assistant' && item.id !== finalAssistantId)
    ) {
      flushSteps();
      if (item.text) {
        timeline.push({ kind: 'note', id: item.id, text: item.text });
      }
    }
  }
  flushSteps();
  return timeline;
}

function toolStep(
  tool: Pick<
    LiveToolActivity,
    | 'id'
    | 'name'
    | 'input'
    | 'output'
    | 'approval'
    | 'userInput'
    | 'mcpElicitation'
    | 'commandOutput'
    | 'mcp'
  >,
  status: ActivityStatus,
): AgentActivityStep {
  const userInput = userInputActivity(tool.id, tool.name, tool.input, tool.output, tool.userInput);
  const mcp = mcpActivity(tool);
  const normalizedName = tool.name.toLowerCase();
  const sessionNotes = sessionNotesActivity(tool.name, tool.input, tool.output);
  return {
    id: tool.id,
    toolName: normalizedName,
    label:
      normalizedName === 'context_compaction'
        ? contextCompactionLabel(status)
        : sessionNotes
          ? sessionNotesLabel(sessionNotes, status)
          : mcp
            ? `Run ${mcp.toolTitle || mcp.toolName}`
            : tool.approval?.kind === 'mcp_tool'
              ? `Run ${tool.approval.target}`
              : toolLabel(tool.name, tool.input),
    icon: mcp || tool.approval?.kind === 'mcp_tool' ? 'extension' : toolIcon(tool.name),
    status,
    input: tool.input,
    output: tool.output,
    approval: tool.approval,
    userInput,
    mcpElicitation: tool.mcpElicitation,
    command: mcp
      ? undefined
      : commandActivity(tool.name, tool.input, tool.output, tool.commandOutput),
    fetch: fetchActivity(tool.name, tool.input, tool.output),
    sessionNotes,
    mcp,
    fileChanges: fileEditActivity(tool.name, tool.input, tool.output),
  };
}

function mergeMCPDetails(
  current: MCPActivityDetails | undefined,
  event: {
    serverId: string;
    serverName: string;
    toolName?: string;
    toolTitle?: string;
    cancelable?: boolean;
  },
): MCPActivityDetails {
  return {
    serverId: event.serverId || current?.serverId || '',
    serverName: event.serverName || current?.serverName || 'MCP server',
    toolName: event.toolName || current?.toolName || 'tool',
    toolTitle: event.toolTitle || current?.toolTitle,
    uiResourceUri: current?.uiResourceUri,
    cancelable: event.cancelable ?? current?.cancelable ?? false,
    cancelling: current?.cancelling ?? false,
    message: current?.message,
    progress: current?.progress,
    total: current?.total,
    logs: current?.logs ?? [],
    content: current?.content ?? [],
    structuredContent: current?.structuredContent,
    isError: current?.isError ?? false,
    error: current?.error,
  };
}

function mcpActivity(
  tool: Pick<LiveToolActivity, 'name' | 'input' | 'output' | 'mcp'>,
): MCPActivityDetails | undefined {
  const metadata = recordValue(tool.output?.['mcp']);
  const acpServerName = stringValue(tool.input['server']);
  const acpToolName = stringValue(tool.input['tool']);
  const acpArguments = recordValue(tool.input['arguments']);
  const isACPMCPTool = acpServerName !== '' && acpToolName !== '' && acpArguments !== null;
  const isMCPTool =
    isACPMCPTool ||
    !!tool.mcp ||
    !!metadata ||
    (!!tool.output && tool.name.toLowerCase().startsWith('mcp_'));
  if (!isMCPTool) {
    return undefined;
  }
  const acpResult = recordValue(tool.output?.['result']);
  const result = acpResult ?? tool.output;
  const content = Array.isArray(result?.['content'])
    ? result['content'].flatMap((item): MCPContentItem[] => {
        const record = recordValue(item);
        const type = stringValue(record?.['type']);
        return record && type ? [{ ...record, type }] : [];
      })
    : [];
  const hasStructuredContent = !!result && Object.hasOwn(result, 'structuredContent');
  const state = stringValue(tool.output?.['state']).toLowerCase();
  const isError =
    booleanValue(metadata?.['isError']) ||
    booleanValue(result?.['isError']) ||
    state === 'failed' ||
    state === 'error' ||
    state === 'timed_out';
  const explicitError =
    stringValue(tool.output?.['error']) ||
    stringValue(result?.['error']) ||
    stringValue(metadata?.['error']);
  const returnedError = isError ? mcpReturnedError(content) : '';
  return {
    serverId: stringValue(metadata?.['serverId']) || tool.mcp?.serverId || acpServerName,
    serverName:
      stringValue(metadata?.['serverName']) ||
      tool.mcp?.serverName ||
      acpServerName ||
      'MCP server',
    toolName: stringValue(metadata?.['toolName']) || tool.mcp?.toolName || acpToolName || tool.name,
    toolTitle: stringValue(metadata?.['toolTitle']) || tool.mcp?.toolTitle,
    uiResourceUri: stringValue(metadata?.['uiResourceUri']) || tool.mcp?.uiResourceUri,
    cancelable: tool.output ? false : (tool.mcp?.cancelable ?? false),
    cancelling: tool.mcp?.cancelling ?? false,
    message: tool.output ? undefined : tool.mcp?.message,
    progress: tool.output ? undefined : tool.mcp?.progress,
    total: tool.output ? undefined : tool.mcp?.total,
    logs: tool.mcp?.logs ?? [],
    content,
    structuredContent: hasStructuredContent
      ? result?.['structuredContent']
      : tool.mcp?.structuredContent,
    isError,
    error:
      explicitError ||
      returnedError ||
      tool.mcp?.error ||
      (isError ? 'MCP tool call failed' : undefined),
  };
}

function mcpReturnedError(content: readonly MCPContentItem[]): string {
  const text = stringValue(content.find((item) => item.type === 'text')?.['text']).trim();
  if (!text) {
    return '';
  }
  let summary = text;
  try {
    const parsed = recordValue(JSON.parse(text));
    summary =
      stringValue(parsed?.['message']) ||
      stringValue(recordValue(parsed?.['error'])?.['message']) ||
      text;
  } catch {
    // MCP text results do not have to be JSON.
  }
  return truncate(summary.replaceAll(/\s+/g, ' '), 240);
}

function toolResultStatus(output: Record<string, unknown> | undefined): ActivityStatus {
  const state = stringValue(output?.['state']).toLowerCase();
  if (state === 'denied') {
    return 'denied';
  }
  if (state === 'cancelled' || state === 'canceled') {
    return 'cancelled';
  }
  if (
    state === 'failed' ||
    state === 'error' ||
    state === 'timed_out' ||
    state === 'conflict' ||
    booleanValue(output?.['timedOut']) ||
    stringValue(output?.['error']) !== ''
  ) {
    return 'failed';
  }
  const httpStatus = numberValueOrUndefined(output?.['httpStatus']);
  if (httpStatus !== undefined && httpStatus >= 400) {
    return 'failed';
  }
  const exitCode = numberValueOrUndefined(output?.['exitCode']);
  return exitCode !== undefined && exitCode !== 0 ? 'failed' : 'complete';
}

function missingResultStatus(runStatus: AgentRun['status'] | undefined): ActivityStatus {
  switch (runStatus) {
    case 'queued':
    case 'running':
      return 'running';
    case 'cancelled':
      return 'cancelled';
    case 'failed':
    case 'interrupted':
      return 'failed';
    default:
      return 'incomplete';
  }
}

function toolLabel(name: string, input: Record<string, unknown>): string {
  const title = stringValue(input['title']).trim();
  if (title) {
    return title;
  }
  const path = typeof input['path'] === 'string' ? input['path'] : '';
  const normalized = name.toLowerCase();

  if (normalized === 'list_directory') {
    return path ? `Inspect ${path}` : 'Inspect directory';
  }
  if (normalized === 'read_file') {
    const startLine = numberValue(input['startLine']);
    const endLine = numberValue(input['endLine']);
    const target = path || 'file';
    if (startLine > 0 && endLine >= startLine) {
      const range = startLine === endLine ? `line ${startLine}` : `lines ${startLine}-${endLine}`;
      return `Read ${target} ${range}`;
    }
    if (startLine > 0) {
      return `Read ${target} from line ${startLine}`;
    }
    if (endLine > 0) {
      return `Read ${target} through line ${endLine}`;
    }
    return path ? `Read ${path}` : 'Read file';
  }
  if (normalized === 'grep') {
    const pattern = truncate(stringValue(input['pattern']).replaceAll(/\s+/g, ' '), 40);
    const target = path ? truncate(path, 32) : 'workspace';
    return pattern ? `Search "${pattern}" in ${target}` : `Search ${target}`;
  }
  if (normalized === 'edit_file') {
    const changes = Array.isArray(input['changes']) ? input['changes'] : [];
    if (changes.length > 1) {
      return `Change ${changes.length} files`;
    }
    if (changes.length === 1) {
      const change = recordValue(changes[0]);
      const changePath = stringValue(change?.['path']);
      const operation = fileChangeOperation(change?.['operation']);
      if (changePath && operation) {
        return `${operationVerb(operation)} ${changePath}`;
      }
    }
    return path ? `Edit ${path}` : 'Edit file';
  }
  if (normalized === 'fetch_url') {
    const rawUrl = typeof input['url'] === 'string' ? input['url'] : '';
    if (!rawUrl) {
      return 'Fetch URL';
    }
    try {
      return `Fetch ${new URL(rawUrl).host}`;
    } catch {
      return 'Fetch URL';
    }
  }
  if (normalized === 'load_skill') {
    const name = stringValue(input['name']);
    const resource = stringValue(input['resource']);
    if (resource && resource !== 'SKILL.md') {
      return name ? `Load ${resource} from ${name}` : `Load ${resource}`;
    }
    return name ? `Load skill ${name}` : 'Load skill';
  }
  if (normalized === 'run_command') {
    const command = stringValue(input['command']);
    const args = stringValues(input['args']);
    const preview = args.length ? formatCommandLine(basename(command), args) : command.trim();
    return preview ? `Run ${truncate(preview, 72)}` : 'Run command';
  }
  if (normalized === 'ask_user') {
    const questions = userQuestions(input['questions']);
    if (questions.length === 1) {
      return `Ask: ${truncate(questions[0].question, 72)}`;
    }
    return questions.length > 1 ? `Ask ${questions.length} questions` : 'Ask for clarification';
  }
  if (normalized === 'acp_elicitation') {
    return 'Request user input';
  }

  return humanize(name);
}

function contextCompactionLabel(status: ActivityStatus): string {
  if (status === 'running') {
    return 'Compacting context';
  }
  return status === 'complete' ? 'Context compacted' : 'Context compaction';
}

function sessionNotesLabel(details: SessionNotesActivityDetails, status: ActivityStatus): string {
  if (details.operation === 'read') {
    return status === 'running' ? 'Reading session notes' : 'Read session notes';
  }
  if (details.state === 'unchanged') {
    return 'Session notes unchanged';
  }
  if (details.state === 'conflict') {
    return 'Session notes update conflicted';
  }
  return status === 'running'
    ? 'Updating session notes'
    : status === 'complete'
      ? 'Updated session notes'
      : 'Update session notes';
}

function toolIcon(name: string): string {
  const normalized = name.toLowerCase();

  if (normalized === 'context_compaction') {
    return 'compress';
  }
  if (normalized === 'read_session_notes') {
    return 'note_alt';
  }
  if (normalized === 'update_session_notes') {
    return 'edit_note';
  }
  if (normalized === 'ask_user') {
    return 'question_answer';
  }
  if (normalized === 'acp_elicitation') {
    return 'contact_support';
  }
  if (normalized.includes('skill')) {
    return 'auto_stories';
  }

  if (normalized.includes('list') || normalized.includes('directory')) {
    return 'folder_open';
  }
  if (normalized.includes('read')) {
    return 'description';
  }
  if (normalized.includes('search') || normalized.includes('find') || normalized.includes('grep')) {
    return 'search';
  }
  if (normalized.includes('write') || normalized.includes('create')) {
    return 'note_add';
  }
  if (normalized.includes('edit') || normalized.includes('update')) {
    return 'edit';
  }
  if (
    normalized.includes('shell') ||
    normalized.includes('command') ||
    normalized.includes('exec')
  ) {
    return 'terminal';
  }
  if (normalized.includes('web') || normalized.includes('http') || normalized.includes('url')) {
    return 'language';
  }

  return 'build';
}

function humanize(value: string): string {
  const words = value.replaceAll(/[_-]+/g, ' ').trim();
  return words ? `${words[0].toUpperCase()}${words.slice(1)}` : 'Tool';
}

function subagentResultText(output: Record<string, unknown> | undefined): string | undefined {
  if (!output) {
    return undefined;
  }
  const result = output['result'] ?? output['output'];
  if (typeof result === 'string') {
    return result.trim() || undefined;
  }
  if (result === undefined || result === null) {
    return undefined;
  }
  try {
    return `\`\`\`json\n${JSON.stringify(result, null, 2)}\n\`\`\``;
  } catch {
    return String(result);
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function stringValues(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : [];
}

function formatCommandLine(command: string, args: readonly string[]): string {
  const rawCommand = command.trim();
  if (args.length === 0) {
    return rawCommand;
  }
  return [rawCommand, ...args].map(quoteBashToken).join(' ');
}

function quoteBashToken(value: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) {
    return value;
  }
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function booleanValue(value: unknown): boolean {
  return value === true;
}

function commandActivity(
  name: string,
  input: Record<string, unknown>,
  output: Record<string, unknown> | undefined,
  liveOutput: LiveCommandOutput | undefined,
): CommandActivityDetails | undefined {
  if (name.toLowerCase() !== 'run_command') {
    return undefined;
  }
  const result = output ?? {};
  const exitCode = numberValueOrUndefined(result['exitCode']);
  const rawCommand = stringValue(result['command']) || stringValue(input['command']);
  const explicitArgs = stringValues(result['args']).length
    ? stringValues(result['args'])
    : stringValues(input['args']);
  const rawError = stringValue(result['error']);
  const title = stringValue(result['title']) || stringValue(input['title']);
  const kind = stringValue(result['kind']) || stringValue(input['kind']);
  const errorIsSyntheticTitle =
    kind === 'execute' && rawError.trim() !== '' && rawError.trim() === title.trim();
  return {
    commandLine: formatCommandLine(rawCommand, explicitArgs),
    workingDirectory:
      stringValue(result['workingDirectory']) || stringValue(input['workingDirectory']) || '.',
    timeoutSeconds:
      numberValue(result['timeoutSeconds']) || numberValue(input['timeoutSeconds']) || 120,
    stdout: output ? stringValue(result['stdout']) : liveOutput?.stdout || '',
    stderr: output ? stringValue(result['stderr']) : liveOutput?.stderr || '',
    state: stringValue(result['state']) || undefined,
    exitCode,
    durationMs: numberValueOrUndefined(result['durationMs']),
    timedOut: booleanValue(result['timedOut']),
    stdoutTruncated: booleanValue(result['stdoutTruncated']),
    stderrTruncated: booleanValue(result['stderrTruncated']),
    error: errorIsSyntheticTitle ? undefined : rawError || undefined,
  };
}

function fetchActivity(
  name: string,
  input: Record<string, unknown>,
  output: Record<string, unknown> | undefined,
): FetchActivityDetails | undefined {
  if (name.toLowerCase() !== 'fetch_url') {
    return undefined;
  }

  return {
    state: stringValue(output?.['state']) || undefined,
    requestedUrl: stringValue(output?.['url']) || stringValue(input['url']),
    finalUrl: stringValue(output?.['finalUrl']) || undefined,
    httpStatus: numberValueOrUndefined(output?.['httpStatus']),
    contentType: stringValue(output?.['contentType']) || undefined,
    truncated: booleanValue(output?.['truncated']),
    reason: stringValue(output?.['reason']) || undefined,
  };
}

function sessionNotesActivity(
  name: string,
  input: Record<string, unknown>,
  output: Record<string, unknown> | undefined,
): SessionNotesActivityDetails | undefined {
  const normalized = name.toLowerCase();
  if (normalized !== 'read_session_notes' && normalized !== 'update_session_notes') {
    return undefined;
  }
  const operation = normalized === 'update_session_notes' ? 'update' : 'read';
  const content =
    operation === 'update' ? stringValue(input['content']) : stringValue(output?.['content']);
  return {
    operation,
    state: stringValue(output?.['state']) || undefined,
    content,
    revision: numberValueOrUndefined(output?.['revision']),
    expectedRevision:
      numberValueOrUndefined(input['expectedRevision']) ??
      numberValueOrUndefined(output?.['expectedRevision']),
    bytes:
      numberValueOrUndefined(output?.['bytes']) ??
      (operation === 'update' || output !== undefined ? utf8ByteLength(content) : undefined),
    updatedAt: stringValue(output?.['updatedAt']) || undefined,
    reason: stringValue(output?.['reason']) || undefined,
  };
}

function fileEditActivity(
  name: string,
  input: Record<string, unknown>,
  output: Record<string, unknown> | undefined,
): FileChangePreview[] | undefined {
  if (name.toLowerCase() !== 'edit_file') {
    return undefined;
  }

  const files = fileChangePreviews(output?.['files']);
  if (files.length > 0) {
    return files;
  }
  const inputFiles = fileChangePreviews(input['files']);
  if (inputFiles.length > 0) {
    return inputFiles;
  }

  const diff = stringValue(output?.['diff']) || stringValue(input['diff']);
  const diffFiles = unifiedDiffFileChanges(diff);
  if (diffFiles.length > 0) {
    return diffFiles;
  }
  const path = stringValue(output?.['path']) || stringValue(input['path']);
  return diff && path ? [{ operation: 'update', path, diff }] : undefined;
}

function userInputActivity(
  toolCallId: string,
  name: string,
  input: Record<string, unknown>,
  output: Record<string, unknown> | undefined,
  live: UserInputState | undefined,
): UserInputState | undefined {
  if (name.toLowerCase() !== 'ask_user') {
    return undefined;
  }

  const answers = userInputAnswers(output?.['answers']);
  if (live) {
    return {
      ...live,
      questions: live.questions.length > 0 ? live.questions : userQuestions(input['questions']),
      status: answers.length > 0 ? 'answered' : live.status,
      ...(answers.length > 0 ? { answers } : {}),
    };
  }

  const questions = userQuestions(input['questions']);
  if (questions.length === 0 || answers.length === 0) {
    return undefined;
  }
  return {
    id: `persisted:${toolCallId}`,
    toolCallId,
    toolName: 'ask_user',
    questions,
    status: 'answered',
    answers,
  };
}

function numberValueOrUndefined(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function basename(path: string): string {
  const normalized = path.replaceAll('\\', '/');
  return normalized.slice(normalized.lastIndexOf('/') + 1);
}

function truncate(value: string, maximum: number): string {
  return value.length <= maximum ? value : `${value.slice(0, maximum - 3)}...`;
}

function appendCommandOutput(current: string, addition: string): string {
  const available = maxLiveCommandOutputCharacters - current.length;
  return available <= 0 ? current : current + addition.slice(0, available);
}

function fileChangePreviews(value: unknown): FileChangePreview[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const previews: FileChangePreview[] = [];
  for (const item of value) {
    const record = recordValue(item);
    const operation = fileChangeOperation(record?.['operation']);
    const path = stringValue(record?.['path']);
    if (!record || !operation || !path) {
      continue;
    }
    previews.push({ operation, path, diff: stringValue(record['diff']) });
  }
  return previews;
}

function unifiedDiffFileChanges(diff: string): FileChangePreview[] {
  if (!diff) {
    return [];
  }
  const headers = Array.from(diff.matchAll(/^--- ([^\r\n]+)\r?\n\+\+\+ ([^\r\n]+)(?:\r?\n|$)/gm));
  return headers.flatMap((header, index): FileChangePreview[] => {
    const oldPath = header[1];
    const newPath = header[2];
    const operation: FileChangeOperation =
      oldPath === '/dev/null' ? 'create' : newPath === '/dev/null' ? 'delete' : 'update';
    const path = unifiedDiffPath(operation === 'delete' ? oldPath : newPath);
    if (!path || header.index === undefined) {
      return [];
    }
    const end = headers[index + 1]?.index ?? diff.length;
    const fileDiff = `${diff.slice(header.index, end).replace(/(?:\r?\n)+$/, '')}\n`;
    return [{ operation, path, diff: fileDiff }];
  });
}

function unifiedDiffPath(path: string): string {
  return path.startsWith('a/') || path.startsWith('b/') ? path.slice(2) : path;
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function agentPlanFromTranscript(item: TranscriptItem): AgentPlan | null {
  if (item.kind === 'plan') {
    return {
      id: item.id,
      entries: agentPlanEntries(item.toolOutput?.['entries']),
    };
  }
  if (item.kind !== 'thought' || !item.text) {
    return null;
  }

  const [heading, ...lines] = item.text.split(/\r?\n/);
  if (heading.trim().toLowerCase() !== 'plan') {
    return null;
  }
  const entries = lines.flatMap((line): AgentPlanEntry[] => {
    const match = /^-\s+\[([ xX>])\]\s+(.+)$/.exec(line.trim());
    if (!match) {
      return [];
    }
    const marker = match[1].toLowerCase();
    return [
      {
        content: match[2].trim(),
        priority: 'medium',
        status: marker === 'x' ? 'completed' : marker === '>' ? 'in_progress' : 'pending',
      },
    ];
  });
  return entries.length > 0 ? { id: item.id, entries } : null;
}

function agentPlanEntries(value: unknown): AgentPlanEntry[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item): AgentPlanEntry[] => {
    const entry = recordValue(item);
    const content = stringValue(entry?.['content']).trim();
    if (!content) {
      return [];
    }
    const rawPriority = stringValue(entry?.['priority']);
    const rawStatus = stringValue(entry?.['status']);
    return [
      {
        content,
        priority:
          rawPriority === 'low' || rawPriority === 'high' ? rawPriority : ('medium' as const),
        status:
          rawStatus === 'completed' || rawStatus === 'in_progress'
            ? rawStatus
            : ('pending' as const),
      },
    ];
  });
}

function userQuestions(value: unknown): UserQuestion[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) => {
    const question = recordValue(item);
    const id = stringValue(question?.['id']);
    const text = stringValue(question?.['question']);
    if (!id || !text) {
      return [];
    }
    const options = Array.isArray(question?.['options'])
      ? question['options'].flatMap((rawOption) => {
          const option = recordValue(rawOption);
          const optionId = stringValue(option?.['id']);
          const label = stringValue(option?.['label']);
          return optionId && label
            ? [
                {
                  id: optionId,
                  label,
                  description: stringValue(option?.['description']) || undefined,
                },
              ]
            : [];
        })
      : [];
    return [{ id, question: text, options }];
  });
}

function userInputAnswers(value: unknown): UserInputAnswer[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) => {
    const answer = recordValue(item);
    const questionId = stringValue(answer?.['questionId']);
    const text = stringValue(answer?.['answer']);
    return questionId && text
      ? [
          {
            questionId,
            answer: text,
            optionId: stringValue(answer?.['optionId']) || undefined,
          },
        ]
      : [];
  });
}

function fileChangeOperation(value: unknown): FileChangeOperation | null {
  return value === 'create' || value === 'update' || value === 'delete' ? value : null;
}

function operationVerb(operation: FileChangeOperation): string {
  switch (operation) {
    case 'create':
      return 'Create';
    case 'delete':
      return 'Delete';
    default:
      return 'Update';
  }
}
