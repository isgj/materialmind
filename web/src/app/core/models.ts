export interface Workspace {
  id: string;
  name: string;
  rootPath: string;
  available: boolean;
  createdAt: string;
  updatedAt: string;
}

export type LlmProviderAuthType = 'none' | 'bearer_env' | 'bearer_keyring';

export interface LlmProviderInput {
  name: string;
  apiCompatibility: string;
  baseUrl: string;
  authType: LlmProviderAuthType;
  bearerTokenEnvVar: string;
  bearerToken: string;
}

export interface LlmProvider extends Omit<
  LlmProviderInput,
  'baseUrl' | 'bearerTokenEnvVar' | 'bearerToken'
> {
  id: string;
  baseUrl?: string;
  bearerTokenEnvVar?: string;
  credentialAvailable: boolean;
  credentialBackend?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AcpAgent {
  id: string;
  name: string;
  command: string;
  arguments: string[];
  available: boolean;
  resolvedCommand?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AcpAgentImplementation {
  name: string;
  title?: string;
  version: string;
}

export interface AcpAgentAuthVariable {
  name: string;
  label?: string;
  optional: boolean;
  secret: boolean;
}

export interface AcpAgentAuthMethod {
  id: string;
  name: string;
  type: 'agent' | 'env_var' | 'terminal';
  description?: string;
  link?: string;
  variables: AcpAgentAuthVariable[];
  supported: boolean;
}

export interface AcpAgentInspection {
  protocolVersion: number;
  implementation?: AcpAgentImplementation;
  authMethods: AcpAgentAuthMethod[];
  logout: boolean;
  sessions: {
    list: boolean;
    load: boolean;
    resume: boolean;
    close: boolean;
    additionalDirectories: boolean;
  };
  promptImage: boolean;
  promptAudio: boolean;
  embeddedContext: boolean;
  mcpStdio: boolean;
  mcpHttp: boolean;
  mcpSse: boolean;
}

export interface AcpRemoteSession {
  id: string;
  workingDirectory: string;
  title?: string;
  updatedAt?: string;
  additionalDirectories: string[];
}

export type McpTransport = 'stdio' | 'http';
export type McpAuthType = 'none' | 'bearer_env' | 'oauth';
export type McpOAuthClientMode = 'dynamic' | 'pre_registered';

export interface McpVariableBinding {
  name: string;
  valueEnvVar: string;
}

export interface McpServerInput {
  name: string;
  transport: McpTransport;
  command: string;
  arguments: string[];
  environment: McpVariableBinding[];
  url: string;
  headers: McpVariableBinding[];
  authType: McpAuthType;
  bearerTokenEnvVar: string;
  oauthClientMode: McpOAuthClientMode;
  oauthClientId: string;
  oauthClientSecretEnvVar: string;
  oauthScopes: string[];
}

export interface McpServer extends McpServerInput {
  id: string;
  defaultEnabled: boolean;
  defaultConfirmationMode: ToolConfirmationMode;
  defaultToolPermissions: McpToolPermission[];
  available: boolean;
  credentialAvailable: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface McpToolSummary {
  name: string;
  title?: string;
  description?: string;
}

export interface McpToolCatalog {
  protocolVersion: string;
  serverName?: string;
  serverVersion?: string;
  tools: McpToolSummary[];
  prompts: McpPromptSummary[];
  resources: McpResourceSummary[];
  resourceTemplates: McpResourceTemplateSummary[];
  extensions?: Record<string, unknown>;
}

export interface McpPromptArgument {
  name: string;
  title?: string;
  description?: string;
  required: boolean;
}

export interface McpPromptSummary {
  name: string;
  title?: string;
  description?: string;
  arguments: McpPromptArgument[];
}

export interface McpResourceSummary {
  uri: string;
  name: string;
  title?: string;
  description?: string;
  mimeType?: string;
  size?: number;
}

export interface McpResourceTemplateSummary {
  uriTemplate: string;
  name: string;
  title?: string;
  description?: string;
  mimeType?: string;
}

export interface McpSessionContentServer {
  id: string;
  name: string;
  prompts: McpPromptSummary[];
  resources: McpResourceSummary[];
  resourceTemplates: McpResourceTemplateSummary[];
  error?: string;
}

export interface McpResourceContent {
  uri: string;
  mimeType?: string;
  text?: string;
  blob?: string;
  meta?: Record<string, unknown>;
}

export interface McpResourceRead {
  serverId: string;
  serverName: string;
  uri: string;
  contents: McpResourceContent[];
}

export interface McpPromptMessage {
  role: 'user' | 'assistant' | string;
  content: Record<string, unknown>;
}

export interface McpPromptExpansion {
  serverId: string;
  serverName: string;
  name: string;
  description?: string;
  messages: McpPromptMessage[];
}

export interface McpToolPermission {
  toolName: string;
  confirmationMode: ToolConfirmationMode;
}

export interface McpServerAssignment {
  server: McpServer;
  enabled: boolean;
  confirmationMode: ToolConfirmationMode;
  toolPermissions: McpToolPermission[];
}

export type McpOAuthState = 'not_applicable' | 'disconnected' | 'pending' | 'connected' | 'error';

export interface McpOAuthStatus {
  state: McpOAuthState;
  error?: string;
  credentialStorage: 'os_keyring' | 'memory' | string;
}

export interface McpOAuthStart {
  authorizationUrl?: string;
  state?: string;
  connected: boolean;
}

export interface AcpConfigSelectValue {
  name: string;
  value: string;
  description?: string;
}

export interface AcpConfigSelectGroup {
  group: string;
  name: string;
  options: AcpConfigSelectValue[];
}

export interface AcpConfigSelectOption {
  type: 'select';
  id: string;
  name: string;
  description?: string;
  category?: string;
  currentValue: string;
  options: Array<AcpConfigSelectValue | AcpConfigSelectGroup>;
}

export interface AcpConfigBooleanOption {
  type: 'boolean';
  id: string;
  name: string;
  description?: string;
  category?: string;
  currentValue: boolean;
}

export type AcpConfigOption = AcpConfigSelectOption | AcpConfigBooleanOption;

export type ReasoningEffort =
  'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra';

export interface LlmGenerationSettings {
  contextWindowTokens: number;
  maxOutputTokens: number;
  reasoningEffort: ReasoningEffort | null;
}

export interface LlmModelInput extends LlmGenerationSettings {
  llmProviderId: string;
  name: string;
  modelId: string;
}

export interface LlmModel extends LlmModelInput {
  id: string;
  createdAt: string;
  updatedAt: string;
}

export interface AvailableLlmModel {
  id: string;
  displayName?: string;
  ownedBy?: string;
  contextWindowTokens?: number;
  maxOutputTokens?: number;
}

export type SessionStatus = 'idle' | 'queued' | 'running' | 'waiting';
export type SessionRuntimeType = 'adk' | 'acp';

export interface AppSession {
  id: string;
  workspaceId: string;
  title: string;
  runtimeType: SessionRuntimeType;
  selectedLlmModelId: string | null;
  acpAgentId: string | null;
  acpSessionId?: string;
  acpConfigOptions: AcpConfigOption[];
  status: SessionStatus;
  createdAt: string;
  updatedAt: string;
}

export interface SessionNotes {
  sessionId: string;
  content: string;
  revision: number;
  updatedAt?: string;
}

export type RunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted';

export interface MessageAttachment {
  id: string;
  name: string;
  mimeType: string;
  size: number;
  createdAt: string;
}

export interface AgentRun {
  id: string;
  sessionId: string;
  invocationId?: string;
  status: RunStatus;
  runtimeType: SessionRuntimeType;
  acpAgentId?: string;
  acpAgentName?: string;
  llmProviderId: string;
  llmProviderName: string;
  llmModelId: string;
  llmModelName: string;
  apiCompatibility: string;
  modelId: string;
  contextWindowTokens: number;
  maxOutputTokens: number;
  reasoningEffort: ReasoningEffort | null;
  baseUrl?: string;
  bearerTokenEnvVar?: string;
  userMessage: string;
  attachments?: MessageAttachment[];
  error?: string;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
}

export type TranscriptKind =
  | 'message'
  | 'thought'
  | 'plan'
  | 'context_compaction'
  | 'tool_call'
  | 'tool_result'
  | 'subagent_call'
  | 'subagent_result';

export interface TranscriptItem {
  id: string;
  invocationId?: string;
  kind: TranscriptKind;
  role?: 'user' | 'assistant';
  text?: string;
  toolName?: string;
  toolCallId?: string;
  toolInput?: Record<string, unknown>;
  toolOutput?: Record<string, unknown>;
  agentName?: string;
  agentLabel?: string;
  agentPath?: string;
  delegationId?: string;
  provider?: string;
  model?: string;
  attachments?: MessageAttachment[];
  createdAt: string;
}

export interface TranscriptPage {
  items: TranscriptItem[];
  nextCursor?: number;
  hasMore: boolean;
}

export interface StorageSettings {
  retentionDays: number;
}

export interface AgentEventContext {
  agentName?: string;
  agentLabel?: string;
  agentPath?: string;
  delegationId?: string;
}

export interface ApiError {
  error: {
    code: string;
    message: string;
  };
}

export interface StreamMessage extends AgentEventContext {
  id?: string;
  text: string;
}

export interface StreamThought {
  id: string;
  text: string;
}

export type AgentPlanEntryStatus = 'pending' | 'in_progress' | 'completed';
export type AgentPlanEntryPriority = 'low' | 'medium' | 'high';

export interface AgentPlanEntry {
  content: string;
  priority: AgentPlanEntryPriority;
  status: AgentPlanEntryStatus;
}

export interface AgentPlan {
  id: string;
  entries: AgentPlanEntry[];
}

export interface StreamToolCall extends AgentEventContext {
  id: string;
  name: string;
  input: Record<string, unknown>;
}

export type ToolCallStatus = 'pending' | 'in_progress' | 'completed' | 'failed';

export interface StreamToolStatus {
  id: string;
  status: ToolCallStatus;
}

export interface StreamToolResult extends AgentEventContext {
  id: string;
  name: string;
  output: Record<string, unknown>;
}

export interface StreamSubagentStarted {
  id: string;
  name: string;
  label: string;
  task: string;
}

export interface StreamSubagentCompleted {
  id: string;
  name: string;
  label: string;
  output: Record<string, unknown>;
}

export interface StreamCommandOutput {
  toolCallId: string;
  stream: 'stdout' | 'stderr';
  text: string;
}

export interface StreamContextCompaction {
  id: string;
  status: 'running' | 'completed' | 'failed' | 'cancelled';
  estimatedTokensBefore: number;
  estimatedTokensAfter?: number;
  maxContextTokens: number;
  summarizedContents: number;
  error?: string;
}

export interface StreamACPUsage {
  used: number;
  size: number;
  percentage: number;
  cost?: {
    amount: number;
    currency: string;
  };
}

export interface StreamMCPCallStarted {
  sessionId: string;
  toolCallId: string;
  serverId: string;
  serverName: string;
  toolName: string;
  toolTitle?: string;
  cancelable: boolean;
}

export interface StreamMCPCallFinished extends StreamMCPCallStarted {
  output: Record<string, unknown>;
}

export interface StreamMCPProgress extends StreamMCPCallStarted {
  message?: string;
  progress: number;
  total?: number;
}

export interface StreamMCPLog {
  sessionId: string;
  toolCallId?: string;
  serverId: string;
  serverName: string;
  toolName?: string;
  toolTitle?: string;
  level: string;
  logger?: string;
  data: unknown;
}

export interface StreamMCPToolsChanged {
  sessionId: string;
  serverId: string;
  serverName: string;
  added?: string[];
  removed?: string[];
  count?: number;
  error?: string;
}

export interface StreamMCPServerUnavailable {
  sessionId: string;
  serverId: string;
  serverName: string;
  error: string;
}

export type MCPElicitationMode = 'form' | 'url';
export type MCPElicitationAction = 'accept' | 'decline' | 'cancel';

export interface StreamMCPElicitationRequest {
  id: string;
  /** The protocol that originated the otherwise shared user-input request. */
  source?: 'mcp' | 'acp';
  sessionId: string;
  toolCallId: string;
  serverId: string;
  serverName: string;
  mode: MCPElicitationMode;
  message: string;
  url?: string;
  elicitationId?: string;
  requestedSchema?: unknown;
}

export interface MCPElicitationResolution {
  id: string;
  toolCallId: string;
  action: MCPElicitationAction;
  content?: Record<string, unknown>;
}

export interface StreamACPElicitationCompletion {
  id: string;
  elicitationId: string;
}

export interface MCPToolCancellation {
  toolCallId: string;
  cancelled: boolean;
}

export interface StreamToolApproval {
  id: string;
  toolCallId: string;
  toolName: string;
  input: Record<string, unknown>;
  payload?: Record<string, unknown>;
  url?: string;
  hint?: string;
  options?: ToolApprovalOption[];
}

export interface StreamToolApprovalStarted {
  id: string;
  toolCallId: string;
}

export interface UserQuestionOption {
  id: string;
  label: string;
  description?: string;
}

export interface UserQuestion {
  id: string;
  question: string;
  options: UserQuestionOption[];
}

export interface StreamUserInputRequest {
  id: string;
  toolCallId: string;
  toolName: string;
  questions: UserQuestion[];
}

export interface UserInputAnswerSubmission {
  questionId: string;
  optionId?: string;
  text?: string;
}

export interface UserInputAnswer {
  questionId: string;
  answer: string;
  optionId?: string;
}

export interface UserInputResolution {
  id: string;
  toolCallId: string;
  answers: UserInputAnswer[];
}

export interface ToolApprovalOption {
  id: string;
  name: string;
  kind: 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always' | string;
}

export interface ToolApprovalResolution {
  id: string;
  toolCallId: string;
  approved: boolean;
  reason?: string;
  optionId?: string;
}

export type ToolConfirmationMode = 'allow' | 'ask';
export type FilesystemScope = '' | 'workspace' | 'repository' | 'computer';
export type ToolTargetMatcher = 'exact_url' | 'origin';

export interface ToolTargetRule {
  matcher: ToolTargetMatcher;
  target: string;
  confirmationMode: ToolConfirmationMode;
}

export interface ToolPermission {
  toolName: string;
  confirmationMode: ToolConfirmationMode;
  filesystemScope: FilesystemScope;
  targetRules: ToolTargetRule[];
}

export interface ToolDefinition {
  name: string;
  label: string;
  description: string;
  defaultConfirmation: ToolConfirmationMode;
  defaultFilesystemScope?: FilesystemScope;
  supportedScopes: FilesystemScope[];
  supportedTargetMatchers: ToolTargetMatcher[];
}

export interface ToolPermissionSet {
  ownerType: 'workspace' | 'session';
  ownerId: string;
  ownerName: string;
  workspaceId: string;
  workspaceName: string;
  workspaceRoot: string;
  repositoryRoot?: string;
  sessionStatus?: SessionStatus;
  definitions: ToolDefinition[];
  permissions: ToolPermission[];
}
