import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Service, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';

import {
  AcpAgent,
  AcpAgentInspection,
  AcpRemoteSession,
  AgentRun,
  ApiError,
  AppSession,
  AvailableLlmModel,
  LlmModel,
  LlmModelInput,
  LlmProvider,
  LlmProviderInput,
  MCPToolCancellation,
  MCPElicitationAction,
  MCPElicitationResolution,
  McpOAuthStart,
  McpOAuthStatus,
  McpServer,
  McpServerAssignment,
  McpServerInput,
  McpSessionContentServer,
  McpPromptExpansion,
  McpResourceRead,
  McpToolCatalog,
  McpToolPermission,
  ReasoningEffort,
  SessionNotes,
  StorageSettings,
  ToolApprovalResolution,
  ToolPermission,
  ToolPermissionSet,
  TranscriptItem,
  TranscriptPage,
  UserInputAnswerSubmission,
  UserInputResolution,
  Workspace,
} from './models';

@Service()
export class ApiService {
  private readonly http = inject(HttpClient);

  listWorkspaces(): Promise<Workspace[]> {
    return firstValueFrom(this.http.get<Workspace[]>('/api/workspaces'));
  }

  createWorkspace(request: { name: string; rootPath: string }): Promise<Workspace> {
    return firstValueFrom(this.http.post<Workspace>('/api/workspaces', request));
  }

  updateWorkspace(id: string, name: string): Promise<Workspace> {
    return firstValueFrom(this.http.patch<Workspace>(`/api/workspaces/${id}`, { name }));
  }

  deleteWorkspace(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/workspaces/${id}`));
  }

  getWorkspaceToolPermissions(id: string): Promise<ToolPermissionSet> {
    return firstValueFrom(
      this.http.get<ToolPermissionSet>(`/api/workspaces/${id}/tool-permissions`),
    );
  }

  replaceWorkspaceToolPermissions(
    id: string,
    permissions: ToolPermission[],
  ): Promise<ToolPermissionSet> {
    return firstValueFrom(
      this.http.put<ToolPermissionSet>(`/api/workspaces/${id}/tool-permissions`, {
        permissions,
      }),
    );
  }

  listLlmProviders(): Promise<LlmProvider[]> {
    return firstValueFrom(this.http.get<LlmProvider[]>('/api/llm-providers'));
  }

  createLlmProvider(request: LlmProviderInput): Promise<LlmProvider> {
    return firstValueFrom(this.http.post<LlmProvider>('/api/llm-providers', request));
  }

  updateLlmProvider(id: string, request: LlmProviderInput): Promise<LlmProvider> {
    return firstValueFrom(this.http.patch<LlmProvider>(`/api/llm-providers/${id}`, request));
  }

  deleteLlmProvider(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/llm-providers/${id}`));
  }

  listAvailableLlmModels(providerId: string): Promise<AvailableLlmModel[]> {
    return firstValueFrom(
      this.http.get<AvailableLlmModel[]>(`/api/llm-providers/${providerId}/available-models`),
    );
  }

  listLlmModels(): Promise<LlmModel[]> {
    return firstValueFrom(this.http.get<LlmModel[]>('/api/llm-models'));
  }

  createLlmModel(request: LlmModelInput): Promise<LlmModel> {
    return firstValueFrom(this.http.post<LlmModel>('/api/llm-models', request));
  }

  updateLlmModel(id: string, request: LlmModelInput): Promise<LlmModel> {
    return firstValueFrom(this.http.patch<LlmModel>(`/api/llm-models/${id}`, request));
  }

  deleteLlmModel(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/llm-models/${id}`));
  }

  listAcpAgents(): Promise<AcpAgent[]> {
    return firstValueFrom(this.http.get<AcpAgent[]>('/api/acp-agents'));
  }

  createAcpAgent(request: {
    name: string;
    command: string;
    arguments: string[];
  }): Promise<AcpAgent> {
    return firstValueFrom(this.http.post<AcpAgent>('/api/acp-agents', request));
  }

  updateAcpAgent(
    id: string,
    request: { name: string; command: string; arguments: string[] },
  ): Promise<AcpAgent> {
    return firstValueFrom(this.http.patch<AcpAgent>(`/api/acp-agents/${id}`, request));
  }

  deleteAcpAgent(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/acp-agents/${id}`));
  }

  inspectAcpAgent(id: string): Promise<AcpAgentInspection> {
    return firstValueFrom(
      this.http.get<AcpAgentInspection>(`/api/acp-agents/${encodeURIComponent(id)}/capabilities`),
    );
  }

  authenticateAcpAgent(id: string, methodId: string): Promise<AcpAgentInspection> {
    return firstValueFrom(
      this.http.post<AcpAgentInspection>(`/api/acp-agents/${encodeURIComponent(id)}/authenticate`, {
        methodId,
      }),
    );
  }

  logoutAcpAgent(id: string): Promise<AcpAgentInspection> {
    return firstValueFrom(
      this.http.post<AcpAgentInspection>(`/api/acp-agents/${encodeURIComponent(id)}/logout`, {}),
    );
  }

  listAcpAgentSessions(id: string, cwd = ''): Promise<AcpRemoteSession[]> {
    const parameters = cwd ? { params: { cwd } } : {};
    return firstValueFrom(
      this.http.get<AcpRemoteSession[]>(
        `/api/acp-agents/${encodeURIComponent(id)}/sessions`,
        parameters,
      ),
    );
  }

  importAcpAgentSession(
    id: string,
    request: { remoteSessionId: string; workspaceId: string; title: string },
  ): Promise<AppSession> {
    return firstValueFrom(
      this.http.post<AppSession>(
        `/api/acp-agents/${encodeURIComponent(id)}/sessions/import`,
        request,
      ),
    );
  }

  listMcpServers(): Promise<McpServer[]> {
    return firstValueFrom(this.http.get<McpServer[]>('/api/mcp-servers'));
  }

  createMcpServer(request: McpServerInput): Promise<McpServer> {
    return firstValueFrom(this.http.post<McpServer>('/api/mcp-servers', request));
  }

  updateMcpServer(id: string, request: McpServerInput): Promise<McpServer> {
    return firstValueFrom(this.http.patch<McpServer>(`/api/mcp-servers/${id}`, request));
  }

  deleteMcpServer(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/mcp-servers/${id}`));
  }

  updateMcpServerDefaults(
    id: string,
    request: {
      enabled: boolean;
      confirmationMode: 'allow' | 'ask';
      toolPermissions: McpToolPermission[];
    },
  ): Promise<McpServer> {
    return firstValueFrom(this.http.put<McpServer>(`/api/mcp-servers/${id}/defaults`, request));
  }

  listMcpServerTools(id: string): Promise<McpToolCatalog> {
    return firstValueFrom(this.http.get<McpToolCatalog>(`/api/mcp-servers/${id}/tools`));
  }

  startMcpOAuth(id: string): Promise<McpOAuthStart> {
    return firstValueFrom(this.http.post<McpOAuthStart>(`/api/mcp-servers/${id}/oauth/start`, {}));
  }

  getMcpOAuthStatus(id: string): Promise<McpOAuthStatus> {
    return firstValueFrom(this.http.get<McpOAuthStatus>(`/api/mcp-servers/${id}/oauth/status`));
  }

  disconnectMcpOAuth(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/mcp-servers/${id}/oauth`));
  }

  getWorkspaceMcpServers(id: string): Promise<McpServerAssignment[]> {
    return firstValueFrom(
      this.http.get<McpServerAssignment[]>(`/api/workspaces/${id}/mcp-servers`),
    );
  }

  replaceWorkspaceMcpServers(
    id: string,
    assignments: McpServerAssignment[],
  ): Promise<McpServerAssignment[]> {
    return firstValueFrom(
      this.http.put<McpServerAssignment[]>(`/api/workspaces/${id}/mcp-servers`, {
        assignments: assignmentRequest(assignments),
      }),
    );
  }

  listSessions(workspaceId: string): Promise<AppSession[]> {
    return firstValueFrom(this.http.get<AppSession[]>(`/api/workspaces/${workspaceId}/sessions`));
  }

  listAllSessions(): Promise<AppSession[]> {
    return firstValueFrom(this.http.get<AppSession[]>('/api/sessions'));
  }

  getSession(id: string): Promise<AppSession> {
    return firstValueFrom(this.http.get<AppSession>(`/api/sessions/${id}`));
  }

  getSessionNotes(id: string): Promise<SessionNotes> {
    return firstValueFrom(this.http.get<SessionNotes>(`/api/sessions/${id}/notes`));
  }

  createSession(request: {
    workspaceId: string;
    title: string;
    runtimeType: 'adk' | 'acp';
    llmModelId: string | null;
    acpAgentId: string | null;
  }): Promise<AppSession> {
    return firstValueFrom(this.http.post<AppSession>('/api/sessions', request));
  }

  updateSession(
    id: string,
    request: { title: string; llmModelId: string | null },
  ): Promise<AppSession> {
    return firstValueFrom(this.http.patch<AppSession>(`/api/sessions/${id}`, request));
  }

  deleteSession(id: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(`/api/sessions/${id}`));
  }

  setAcpSessionConfigOption(
    sessionId: string,
    configId: string,
    value: string | boolean,
  ): Promise<AppSession> {
    return firstValueFrom(
      this.http.put<AppSession>(
        `/api/sessions/${encodeURIComponent(sessionId)}/acp-config/${encodeURIComponent(configId)}`,
        { value },
      ),
    );
  }

  getSessionToolPermissions(id: string): Promise<ToolPermissionSet> {
    return firstValueFrom(this.http.get<ToolPermissionSet>(`/api/sessions/${id}/tool-permissions`));
  }

  replaceSessionToolPermissions(
    id: string,
    permissions: ToolPermission[],
  ): Promise<ToolPermissionSet> {
    return firstValueFrom(
      this.http.put<ToolPermissionSet>(`/api/sessions/${id}/tool-permissions`, {
        permissions,
      }),
    );
  }

  getSessionMcpServers(id: string): Promise<McpServerAssignment[]> {
    return firstValueFrom(this.http.get<McpServerAssignment[]>(`/api/sessions/${id}/mcp-servers`));
  }

  replaceSessionMcpServers(
    id: string,
    assignments: McpServerAssignment[],
  ): Promise<McpServerAssignment[]> {
    return firstValueFrom(
      this.http.put<McpServerAssignment[]>(`/api/sessions/${id}/mcp-servers`, {
        assignments: assignmentRequest(assignments),
      }),
    );
  }

  listSessionMcpContent(id: string): Promise<McpSessionContentServer[]> {
    return firstValueFrom(
      this.http.get<McpSessionContentServer[]>(`/api/sessions/${id}/mcp-content`),
    );
  }

  readSessionMcpResource(id: string, serverId: string, uri: string): Promise<McpResourceRead> {
    return firstValueFrom(
      this.http.post<McpResourceRead>(`/api/sessions/${id}/mcp-resources/read`, {
        serverId,
        uri,
      }),
    );
  }

  getSessionMcpPrompt(
    id: string,
    serverId: string,
    name: string,
    args: Record<string, string>,
  ): Promise<McpPromptExpansion> {
    return firstValueFrom(
      this.http.post<McpPromptExpansion>(`/api/sessions/${id}/mcp-prompts/get`, {
        serverId,
        name,
        arguments: args,
      }),
    );
  }

  transcript(sessionId: string): Promise<TranscriptItem[]> {
    return firstValueFrom(this.http.get<TranscriptItem[]>(`/api/sessions/${sessionId}/transcript`));
  }

  transcriptPage(sessionId: string, before?: number, limit = 100): Promise<TranscriptPage> {
    const query = new URLSearchParams({ limit: String(limit) });
    if (before !== undefined) {
      query.set('before', String(before));
    }
    return firstValueFrom(
      this.http.get<TranscriptPage>(`/api/sessions/${sessionId}/transcript-page?${query}`),
    );
  }

  getStorageSettings(): Promise<StorageSettings> {
    return firstValueFrom(this.http.get<StorageSettings>('/api/storage-settings'));
  }

  updateStorageSettings(settings: StorageSettings): Promise<StorageSettings> {
    return firstValueFrom(this.http.put<StorageSettings>('/api/storage-settings', settings));
  }

  async downloadBackup(): Promise<void> {
    const response = await firstValueFrom(
      this.http.get('/api/backup', { observe: 'response', responseType: 'blob' }),
    );
    const disposition = response.headers.get('Content-Disposition') ?? '';
    const filename = disposition.match(/filename="?([^";]+)"?/i)?.[1] ?? 'materialmind-backup.db';
    const url = URL.createObjectURL(response.body ?? new Blob());
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename.replaceAll('/', '-').replaceAll('\\', '-');
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  listRuns(sessionId: string): Promise<AgentRun[]> {
    return firstValueFrom(this.http.get<AgentRun[]>(`/api/sessions/${sessionId}/runs`));
  }

  startRun(
    sessionId: string,
    request: {
      message: string;
      llmModelId: string | null;
      reasoningEffort: ReasoningEffort | null;
      attachments: readonly File[];
    },
  ): Promise<AgentRun> {
    if (request.attachments.length === 0) {
      return firstValueFrom(
        this.http.post<AgentRun>(`/api/sessions/${sessionId}/runs`, {
          message: request.message,
          llmModelId: request.llmModelId,
          reasoningEffort: request.reasoningEffort,
        }),
      );
    }
    const body = new FormData();
    body.append('message', request.message);
    if (request.llmModelId) {
      body.append('llmModelId', request.llmModelId);
    }
    if (request.reasoningEffort) {
      body.append('reasoningEffort', request.reasoningEffort);
    }
    for (const attachment of request.attachments) {
      body.append('files', attachment, attachment.name);
    }
    return firstValueFrom(this.http.post<AgentRun>(`/api/sessions/${sessionId}/runs`, body));
  }

  cancelRun(runId: string): Promise<AgentRun> {
    return firstValueFrom(this.http.post<AgentRun>(`/api/runs/${runId}/cancel`, {}));
  }

  cancelMCPToolCall(runId: string, toolCallId: string): Promise<MCPToolCancellation> {
    return firstValueFrom(
      this.http.post<MCPToolCancellation>(
        `/api/runs/${encodeURIComponent(runId)}/mcp-tools/${encodeURIComponent(toolCallId)}/cancel`,
        {},
      ),
    );
  }

  resolveMCPElicitation(
    runId: string,
    requestId: string,
    action: MCPElicitationAction,
    content?: Record<string, unknown>,
  ): Promise<MCPElicitationResolution> {
    return firstValueFrom(
      this.http.post<MCPElicitationResolution>(
        `/api/runs/${encodeURIComponent(runId)}/mcp-elicitations/${encodeURIComponent(requestId)}`,
        { action, content },
      ),
    );
  }

  resolveToolApproval(
    runId: string,
    approvalId: string,
    request: { approved: boolean; reason: string; optionId?: string },
  ): Promise<ToolApprovalResolution> {
    return firstValueFrom(
      this.http.post<ToolApprovalResolution>(
        `/api/runs/${encodeURIComponent(runId)}/tool-approvals/${encodeURIComponent(approvalId)}`,
        request,
      ),
    );
  }

  resolveUserInput(
    runId: string,
    requestId: string,
    answers: UserInputAnswerSubmission[],
  ): Promise<UserInputResolution> {
    return firstValueFrom(
      this.http.post<UserInputResolution>(
        `/api/runs/${encodeURIComponent(runId)}/user-inputs/${encodeURIComponent(requestId)}`,
        { answers },
      ),
    );
  }
}

function assignmentRequest(assignments: McpServerAssignment[]) {
  return assignments.map((assignment) => ({
    serverId: assignment.server.id,
    enabled: assignment.enabled,
    confirmationMode: assignment.confirmationMode,
    toolPermissions: assignment.toolPermissions,
  }));
}

export function errorMessage(error: unknown): string {
  if (error instanceof HttpErrorResponse) {
    const body = error.error as Partial<ApiError> | { error?: string } | string | null;
    if (typeof body === 'object' && body?.error) {
      if (typeof body.error === 'string') {
        return body.error;
      }
      if (typeof body.error.message === 'string') {
        return body.error.message;
      }
    }
    if (typeof body === 'string' && body.trim()) {
      return body;
    }
    return `${error.status} ${error.statusText}`.trim();
  }
  return error instanceof Error ? error.message : 'Unexpected error';
}
