import { Service, computed, inject, signal } from '@angular/core';

import { ApiService } from './api.service';
import {
  AcpAgent,
  AppSession,
  LlmModel,
  LlmProvider,
  McpServer,
  SessionStatus,
  Workspace,
} from './models';

const selectedWorkspaceStorageKey = 'materialmind.selectedWorkspace';

function sessionsEqual(current: AppSession[], next: AppSession[]): boolean {
  return (
    current.length === next.length &&
    current.every((session, index) => {
      const candidate = next[index];
      return (
        candidate !== undefined &&
        session.id === candidate.id &&
        session.workspaceId === candidate.workspaceId &&
        session.title === candidate.title &&
        session.runtimeType === candidate.runtimeType &&
        session.selectedLlmModelId === candidate.selectedLlmModelId &&
        session.acpAgentId === candidate.acpAgentId &&
        JSON.stringify(session.acpConfigOptions) === JSON.stringify(candidate.acpConfigOptions) &&
        session.status === candidate.status &&
        session.createdAt === candidate.createdAt &&
        session.updatedAt === candidate.updatedAt
      );
    })
  );
}

@Service()
export class AppState {
  private readonly api = inject(ApiService);

  readonly workspaces = signal<Workspace[]>([]);
  readonly allSessions = signal<AppSession[]>([]);
  readonly llmProviders = signal<LlmProvider[]>([]);
  readonly llmModels = signal<LlmModel[]>([]);
  readonly acpAgents = signal<AcpAgent[]>([]);
  readonly mcpServers = signal<McpServer[]>([]);
  readonly selectedWorkspaceId = signal<string | null>(
    window.localStorage.getItem(selectedWorkspaceStorageKey),
  );
  readonly loading = signal(true);

  readonly selectedWorkspace = computed(
    () => this.workspaces().find((item) => item.id === this.selectedWorkspaceId()) ?? null,
  );
  readonly sessions = computed(() => {
    const workspaceId = this.selectedWorkspaceId();
    return workspaceId
      ? this.allSessions().filter((session) => session.workspaceId === workspaceId)
      : [];
  });

  async initialize(): Promise<void> {
    this.loading.set(true);
    try {
      const [workspaces, sessions, providers, models, acpAgents, mcpServers] = await Promise.all([
        this.api.listWorkspaces(),
        this.api.listAllSessions(),
        this.api.listLlmProviders(),
        this.api.listLlmModels(),
        this.api.listAcpAgents(),
        this.api.listMcpServers(),
      ]);
      this.workspaces.set(workspaces ?? []);
      this.allSessions.set(sessions ?? []);
      this.llmProviders.set(providers ?? []);
      this.llmModels.set(models ?? []);
      this.acpAgents.set(acpAgents ?? []);
      this.mcpServers.set(mcpServers ?? []);
      const stored = this.selectedWorkspaceId();
      const selected = workspaces.find((workspace) => workspace.id === stored) ?? workspaces[0];
      await this.selectWorkspace(selected?.id ?? null);
    } finally {
      this.loading.set(false);
    }
  }

  async selectWorkspace(id: string | null): Promise<void> {
    this.selectedWorkspaceId.set(id);
    if (id) {
      window.localStorage.setItem(selectedWorkspaceStorageKey, id);
    } else {
      window.localStorage.removeItem(selectedWorkspaceStorageKey);
    }
  }

  async refreshWorkspaces(): Promise<void> {
    this.workspaces.set((await this.api.listWorkspaces()) ?? []);
  }

  async refreshSessions(): Promise<void> {
    const sessions = (await this.api.listAllSessions()) ?? [];
    this.allSessions.update((current) => {
      return sessionsEqual(current, sessions) ? current : sessions;
    });
  }

  setSessionStatus(sessionId: string, status: SessionStatus): void {
    this.allSessions.update((sessions) => {
      const session = sessions.find((item) => item.id === sessionId);
      if (!session || session.status === status) {
        return sessions;
      }
      return sessions.map((item) => (item.id === sessionId ? { ...item, status } : item));
    });
  }

  async refreshLlmSettings(): Promise<void> {
    const [providers, models] = await Promise.all([
      this.api.listLlmProviders(),
      this.api.listLlmModels(),
    ]);
    this.llmProviders.set(providers ?? []);
    this.llmModels.set(models ?? []);
  }

  async refreshAcpAgents(): Promise<void> {
    this.acpAgents.set((await this.api.listAcpAgents()) ?? []);
  }

  async refreshMcpServers(): Promise<void> {
    this.mcpServers.set((await this.api.listMcpServers()) ?? []);
  }
}
