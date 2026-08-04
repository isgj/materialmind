import { Component, DestroyRef, computed, effect, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatButtonToggleModule } from '@angular/material/button-toggle';
import { MatCardModule } from '@angular/material/card';
import { MatDialog } from '@angular/material/dialog';
import { MatDividerModule } from '@angular/material/divider';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatSelectModule } from '@angular/material/select';
import { MatSlideToggleModule } from '@angular/material/slide-toggle';
import { MatSnackBar } from '@angular/material/snack-bar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { ActivatedRoute } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import {
  AcpAgent,
  LlmModel,
  LlmProvider,
  McpOAuthStatus,
  McpServer,
  McpToolPermission,
  StorageSettings,
  ToolConfirmationMode,
} from '../core/models';
import {
  McpToolPermissionsDialogComponent,
  McpToolPermissionsDialogData,
} from '../permissions/mcp-tool-permissions-dialog.component';
import { formatReasoningEffort, supportsReasoningEffort } from '../core/reasoning-effort';
import { ThemeService } from '../core/theme.service';
import { ConfirmDialogComponent, ConfirmDialogData } from '../shared/confirm-dialog.component';
import {
  AcpAgentDialogComponent,
  AcpAgentDialogData,
  AcpAgentDialogResult,
} from './acp-agent-dialog.component';
import {
  AcpCapabilitiesDialogComponent,
  AcpCapabilitiesDialogData,
} from './acp-capabilities-dialog.component';
import { ModelDialogComponent, ModelDialogData, ModelDialogResult } from './model-dialog.component';
import {
  McpServerDialogComponent,
  McpServerDialogData,
  McpServerDialogResult,
} from './mcp-server-dialog.component';
import {
  ProviderDialogComponent,
  ProviderDialogData,
  ProviderDialogResult,
} from './provider-dialog.component';

type SettingsSection = 'appearance' | 'runtimes' | 'mcp' | 'models' | 'data';

const settingsSectionDetails: Record<SettingsSection, { title: string; description: string }> = {
  appearance: {
    title: 'Appearance',
    description: 'Choose how MaterialMind looks on this device.',
  },
  runtimes: {
    title: 'Agent runtimes',
    description: 'Configure the built-in runtime and Agent Client Protocol processes.',
  },
  mcp: {
    title: 'MCP servers',
    description: 'Manage external tools and the policies copied into new workspaces.',
  },
  models: {
    title: 'LLM providers',
    description: 'Configure compatible APIs and the models available to sessions.',
  },
  data: {
    title: 'Data',
    description: 'Manage conversation retention and database backups.',
  },
};

@Component({
  selector: 'app-settings',
  imports: [
    MatButtonModule,
    MatButtonToggleModule,
    MatCardModule,
    MatDividerModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    MatSlideToggleModule,
    MatTooltipModule,
  ],
  templateUrl: './settings.component.html',
  styleUrl: './settings.component.scss',
})
export class SettingsComponent {
  protected readonly state = inject(AppState);
  protected readonly theme = inject(ThemeService);
  protected readonly section = inject(ActivatedRoute).snapshot.data['section'] as SettingsSection;
  protected readonly sectionDetails = settingsSectionDetails[this.section];
  protected readonly sectionCount = computed(() => {
    switch (this.section) {
      case 'runtimes':
        return this.state.acpAgents().length + 1;
      case 'mcp':
        return this.state.mcpServers().length;
      case 'models':
        return this.state.llmProviders().length;
      default:
        return null;
    }
  });
  private readonly api = inject(ApiService);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);
  private readonly destroyRef = inject(DestroyRef);
  private readonly loadedOAuthStatuses = new Set<string>();
  private destroyed = false;
  protected readonly oauthStatuses = signal<Record<string, McpOAuthStatus>>({});
  protected readonly discoveredToolCounts = signal<Record<string, number>>({});
  protected readonly discoveredContentCounts = signal<Record<string, number>>({});
  protected readonly discoveredProtocolVersions = signal<Record<string, string>>({});
  protected readonly savingMcpDefaultIds = signal<Set<string>>(new Set());
  protected readonly storageSettings = signal<StorageSettings | null>(null);
  protected readonly retentionDays = signal(0);
  protected readonly loadingStorageSettings = signal(false);
  protected readonly savingStorageSettings = signal(false);
  protected readonly downloadingBackup = signal(false);
  protected readonly retentionOptions = [
    { value: 0, label: 'Keep indefinitely' },
    { value: 30, label: '30 days' },
    { value: 90, label: '90 days' },
    { value: 180, label: '180 days' },
    { value: 365, label: '1 year' },
  ] as const;
  protected readonly storageSettingsChanged = computed(
    () => this.storageSettings()?.retentionDays !== this.retentionDays(),
  );

  constructor() {
    this.destroyRef.onDestroy(() => {
      this.destroyed = true;
    });
    effect(() => {
      for (const server of this.state.mcpServers()) {
        if (server.authType !== 'oauth' || this.loadedOAuthStatuses.has(server.id)) {
          continue;
        }
        this.loadedOAuthStatuses.add(server.id);
        void this.refreshOAuthStatus(server);
      }
    });
    if (this.section === 'data') {
      void this.loadStorageSettings();
    }
  }

  protected async saveStorageSettings(): Promise<void> {
    const current = this.storageSettings();
    const retentionDays = this.retentionDays();
    if (this.savingStorageSettings() || !current || !this.storageSettingsChanged()) {
      return;
    }
    if (
      retentionDays > 0 &&
      (current.retentionDays === 0 || retentionDays < current.retentionDays)
    ) {
      const retentionLabel =
        this.retentionOptions.find((option) => option.value === retentionDays)?.label ??
        `${retentionDays} days`;
      const confirmed = await this.confirm({
        title: 'Shorten session retention',
        message: `Idle sessions older than ${retentionLabel.toLowerCase()} will be deleted immediately with their runs and attachments. This cannot be undone.`,
        confirmLabel: 'Save and delete',
      });
      if (!confirmed) {
        return;
      }
    }
    this.savingStorageSettings.set(true);
    try {
      const settings = await this.api.updateStorageSettings({
        retentionDays,
      });
      this.storageSettings.set(settings);
      this.retentionDays.set(settings.retentionDays);
      this.snackBar.open('Retention saved', undefined, { duration: 3000 });
    } catch (error) {
      this.showError(error);
    } finally {
      this.savingStorageSettings.set(false);
    }
  }

  protected async downloadBackup(): Promise<void> {
    if (this.downloadingBackup()) {
      return;
    }
    this.downloadingBackup.set(true);
    try {
      await this.api.downloadBackup();
      this.snackBar.open('Backup downloaded', undefined, { duration: 3000 });
    } catch (error) {
      this.showError(error);
    } finally {
      this.downloadingBackup.set(false);
    }
  }

  protected async addAcpAgent(): Promise<void> {
    const result = await this.openAcpAgentDialog(null);
    if (!result) {
      return;
    }
    try {
      await this.api.createAcpAgent(result);
      await this.state.refreshAcpAgents();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async editAcpAgent(agent: AcpAgent): Promise<void> {
    const result = await this.openAcpAgentDialog(agent);
    if (!result) {
      return;
    }
    try {
      await this.api.updateAcpAgent(agent.id, result);
      await this.state.refreshAcpAgents();
    } catch (error) {
      this.showError(error);
    }
  }

  protected inspectAcpAgent(agent: AcpAgent): void {
    this.dialog.open<AcpCapabilitiesDialogComponent, AcpCapabilitiesDialogData>(
      AcpCapabilitiesDialogComponent,
      {
        data: { agent, workspaces: this.state.workspaces() },
        width: '880px',
        maxWidth: '96vw',
        maxHeight: '92vh',
      },
    );
  }

  protected async deleteAcpAgent(agent: AcpAgent): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Delete ACP agent',
      message: `Delete "${agent.name}"? Agents used by sessions cannot be deleted.`,
      confirmLabel: 'Delete',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteAcpAgent(agent.id);
      await this.state.refreshAcpAgents();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async addMcpServer(): Promise<void> {
    const result = await this.openMcpServerDialog(null);
    if (!result) {
      return;
    }
    try {
      await this.api.createMcpServer(result);
      await this.state.refreshMcpServers();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async editMcpServer(server: McpServer): Promise<void> {
    const result = await this.openMcpServerDialog(server);
    if (!result) {
      return;
    }
    try {
      await this.api.updateMcpServer(server.id, result);
      this.loadedOAuthStatuses.delete(server.id);
      this.oauthStatuses.update((statuses) => {
        const next = { ...statuses };
        delete next[server.id];
        return next;
      });
      await this.state.refreshMcpServers();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async deleteMcpServer(server: McpServer): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Delete MCP server',
      message: `Delete "${server.name}"? Servers assigned to a workspace or session cannot be deleted.`,
      confirmLabel: 'Delete',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteMcpServer(server.id);
      await this.state.refreshMcpServers();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async discoverMcpTools(server: McpServer): Promise<void> {
    try {
      const catalog = await this.api.listMcpServerTools(server.id);
      this.discoveredToolCounts.update((counts) => ({
        ...counts,
        [server.id]: catalog.tools.length,
      }));
      this.discoveredContentCounts.update((counts) => ({
        ...counts,
        [server.id]:
          (catalog.prompts?.length ?? 0) +
          (catalog.resources?.length ?? 0) +
          (catalog.resourceTemplates?.length ?? 0),
      }));
      this.discoveredProtocolVersions.update((versions) => ({
        ...versions,
        [server.id]: catalog.protocolVersion,
      }));
      this.snackBar.open(
        `${catalog.tools.length} tools and ${this.discoveredContentCounts()[server.id]} context items available from ${server.name}`,
        undefined,
        { duration: 4000 },
      );
    } catch (error) {
      this.showError(error);
    }
  }

  protected mcpDefaultEnabled(server: McpServer): boolean {
    return server.defaultEnabled ?? false;
  }

  protected mcpDefaultConfirmation(server: McpServer): ToolConfirmationMode {
    return server.defaultConfirmationMode || 'ask';
  }

  protected mcpDefaultToolPermissions(server: McpServer): McpToolPermission[] {
    return server.defaultToolPermissions ?? [];
  }

  protected mcpDefaultOverrideCount(server: McpServer): number {
    return this.mcpDefaultToolPermissions(server).length;
  }

  protected async setMcpDefaultEnabled(server: McpServer, enabled: boolean): Promise<void> {
    await this.saveMcpDefaults(
      server,
      enabled,
      this.mcpDefaultConfirmation(server),
      this.mcpDefaultToolPermissions(server),
    );
  }

  protected async setMcpDefaultConfirmation(
    server: McpServer,
    confirmationMode: ToolConfirmationMode,
  ): Promise<void> {
    await this.saveMcpDefaults(
      server,
      this.mcpDefaultEnabled(server),
      confirmationMode,
      this.mcpDefaultToolPermissions(server),
    );
  }

  protected async editMcpDefaultToolPermissions(server: McpServer): Promise<void> {
    try {
      const catalog = await this.api.listMcpServerTools(server.id);
      const result = await firstValueFrom(
        this.dialog
          .open<
            McpToolPermissionsDialogComponent,
            McpToolPermissionsDialogData,
            McpToolPermission[]
          >(McpToolPermissionsDialogComponent, {
            data: {
              serverName: `${server.name} default`,
              defaultConfirmation: this.mcpDefaultConfirmation(server),
              tools: catalog.tools,
              permissions: this.mcpDefaultToolPermissions(server),
            },
            width: '820px',
            maxWidth: '96vw',
          })
          .afterClosed(),
      );
      if (result === undefined) {
        return;
      }
      await this.saveMcpDefaults(
        server,
        this.mcpDefaultEnabled(server),
        this.mcpDefaultConfirmation(server),
        result,
      );
    } catch (error) {
      this.showError(error);
    }
  }

  protected async connectMcpOAuth(server: McpServer): Promise<void> {
    this.setOAuthStatus(server.id, {
      state: 'pending',
      credentialStorage: this.oauthStatuses()[server.id]?.credentialStorage ?? 'os_keyring',
    });
    try {
      const result = await this.api.startMcpOAuth(server.id);
      if (result.connected) {
        await this.refreshOAuthStatus(server);
        return;
      }
      if (!result.authorizationUrl) {
        throw new Error('The authorization server did not provide an authorization URL');
      }
      const popup = window.open(
        result.authorizationUrl,
        'materialmind-mcp-oauth',
        'popup,width=720,height=760',
      );
      if (!popup) {
        this.snackBar.open('Allow pop-ups to complete MCP authorization.', 'Dismiss', {
          duration: 7000,
        });
      }
      await this.pollOAuthStatus(server);
    } catch (error) {
      this.showError(error);
      await this.refreshOAuthStatus(server);
    }
  }

  protected async disconnectMcpOAuth(server: McpServer): Promise<void> {
    try {
      await this.api.disconnectMcpOAuth(server.id);
      await this.refreshOAuthStatus(server);
    } catch (error) {
      this.showError(error);
    }
  }

  protected mcpConnection(server: McpServer): string {
    return server.transport === 'stdio'
      ? [server.command, ...server.arguments].join(' ')
      : server.url;
  }

  protected mcpTransportLabel(server: McpServer): string {
    return server.transport === 'stdio' ? 'Local stdio server' : 'Streamable HTTP server';
  }

  protected mcpAuthLabel(server: McpServer): string {
    switch (server.authType) {
      case 'bearer_env':
        return `Bearer from ${server.bearerTokenEnvVar}`;
      case 'oauth':
        return server.oauthClientMode === 'pre_registered'
          ? 'OAuth, pre-registered client'
          : 'OAuth, dynamic registration';
      default:
        return 'No authentication';
    }
  }

  protected mcpStatus(server: McpServer): McpOAuthStatus | null {
    return server.authType === 'oauth' ? (this.oauthStatuses()[server.id] ?? null) : null;
  }

  protected mcpStatusIcon(server: McpServer): string {
    const oauth = this.mcpStatus(server);
    if (oauth) {
      switch (oauth.state) {
        case 'connected':
          return 'check_circle';
        case 'pending':
          return 'progress_activity';
        case 'error':
          return 'error';
        default:
          return 'link_off';
      }
    }
    return server.available ? 'check_circle' : 'error';
  }

  protected mcpStatusLabel(server: McpServer): string {
    const oauth = this.mcpStatus(server);
    if (oauth) {
      switch (oauth.state) {
        case 'connected':
          return oauth.credentialStorage === 'memory'
            ? 'Connected, credentials kept in memory'
            : 'Connected, credentials in OS keyring';
        case 'pending':
          return 'Waiting for authorization';
        case 'error':
          return oauth.error || 'Authorization failed';
        default:
          return 'Not connected';
      }
    }
    if (!server.credentialAvailable) {
      return 'Required environment variable is not set';
    }
    return server.available ? 'Ready' : 'Command unavailable';
  }

  protected canDiscoverMcpTools(server: McpServer): boolean {
    if (!server.available) {
      return false;
    }
    return server.authType !== 'oauth' || this.mcpStatus(server)?.state === 'connected';
  }

  protected async addProvider(): Promise<void> {
    const result = await this.openProviderDialog(null);
    if (!result) {
      return;
    }
    try {
      await this.api.createLlmProvider(result);
      await this.state.refreshLlmSettings();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async editProvider(provider: LlmProvider): Promise<void> {
    const result = await this.openProviderDialog(provider);
    if (!result) {
      return;
    }
    try {
      await this.api.updateLlmProvider(provider.id, result);
      await this.state.refreshLlmSettings();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async deleteProvider(provider: LlmProvider): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Delete LLM provider',
      message: `Delete "${provider.name}"? Providers with models cannot be deleted.`,
      confirmLabel: 'Delete',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteLlmProvider(provider.id);
      await this.state.refreshLlmSettings();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async addModel(provider?: LlmProvider): Promise<void> {
    if (this.state.llmProviders().length === 0) {
      await this.addProvider();
      return;
    }
    const result = await this.openModelDialog(null, provider?.id);
    if (!result) {
      return;
    }
    try {
      await this.api.createLlmModel(result);
      await this.state.refreshLlmSettings();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async editModel(model: LlmModel): Promise<void> {
    const result = await this.openModelDialog(model);
    if (!result) {
      return;
    }
    try {
      await this.api.updateLlmModel(model.id, result);
      await this.state.refreshLlmSettings();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async deleteModel(model: LlmModel): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Delete model',
      message: `Delete "${model.name}"? Existing run history keeps its model details.`,
      confirmLabel: 'Delete',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteLlmModel(model.id);
      await this.state.refreshLlmSettings();
      await this.state.refreshSessions();
    } catch (error) {
      this.showError(error);
    }
  }

  protected modelsForProvider(provider: LlmProvider): LlmModel[] {
    return this.state.llmModels().filter((model) => model.llmProviderId === provider.id);
  }

  protected modelCount(provider: LlmProvider): number {
    return this.modelsForProvider(provider).length;
  }

  protected formatTokens(value: number): string {
    return value.toLocaleString();
  }

  protected readonly reasoningEffortLabel = formatReasoningEffort;
  protected readonly providerSupportsReasoningEffort = (provider: LlmProvider): boolean =>
    supportsReasoningEffort(provider.apiCompatibility);

  protected providerCompatibilityLabel(provider: LlmProvider): string {
    switch (provider.apiCompatibility) {
      case 'anthropic':
        return 'Anthropic compatible';
      case 'gemini':
        return 'Google Gemini API';
      case 'openai-chat-completions':
        return 'OpenAI compatible (Chat Completions)';
      case 'openai-responses':
        return 'OpenAI compatible (Responses)';
      default:
        return provider.apiCompatibility;
    }
  }

  protected providerStatusLabel(provider: LlmProvider): string {
    switch (provider.authType) {
      case 'none':
        return 'No credential configured';
      case 'bearer_env':
        return provider.credentialAvailable
          ? `${provider.bearerTokenEnvVar} is set`
          : `${provider.bearerTokenEnvVar} is not set`;
      case 'bearer_keyring':
        if (!provider.credentialAvailable) {
          return 'No credential is stored';
        }
        return provider.credentialBackend === 'memory'
          ? 'Credential is stored for this process only'
          : 'Credential is stored in the OS keyring';
    }
  }

  protected providerAuthenticationLabel(provider: LlmProvider): string {
    switch (provider.authType) {
      case 'none':
        return 'None';
      case 'bearer_env':
        return `Environment: ${provider.bearerTokenEnvVar}`;
      case 'bearer_keyring':
        return provider.credentialBackend === 'memory' ? 'Process memory' : 'OS keyring';
    }
  }

  protected providerStatusIcon(provider: LlmProvider): string {
    if (provider.authType === 'none') {
      return 'lock_open';
    }
    return provider.credentialAvailable ? 'check_circle' : 'error';
  }

  protected acpCommand(agent: AcpAgent): string {
    return [agent.command, ...agent.arguments].join(' ');
  }

  protected acpStatusLabel(agent: AcpAgent): string {
    return agent.available ? 'Command available' : 'Command unavailable';
  }

  protected acpStatusIcon(agent: AcpAgent): string {
    return agent.available ? 'check_circle' : 'error';
  }

  private openAcpAgentDialog(data: AcpAgentDialogData): Promise<AcpAgentDialogResult | undefined> {
    return firstValueFrom(
      this.dialog
        .open<AcpAgentDialogComponent, AcpAgentDialogData, AcpAgentDialogResult>(
          AcpAgentDialogComponent,
          { data, width: '640px' },
        )
        .afterClosed(),
    );
  }

  private openProviderDialog(data: ProviderDialogData): Promise<ProviderDialogResult | undefined> {
    return firstValueFrom(
      this.dialog
        .open<ProviderDialogComponent, ProviderDialogData, ProviderDialogResult>(
          ProviderDialogComponent,
          { data, width: '640px' },
        )
        .afterClosed(),
    );
  }

  private openMcpServerDialog(
    data: McpServerDialogData,
  ): Promise<McpServerDialogResult | undefined> {
    return firstValueFrom(
      this.dialog
        .open<McpServerDialogComponent, McpServerDialogData, McpServerDialogResult>(
          McpServerDialogComponent,
          {
            data,
            width: '720px',
            maxWidth: '96vw',
          },
        )
        .afterClosed(),
    );
  }

  private openModelDialog(
    model: LlmModel | null,
    initialProviderId?: string,
  ): Promise<ModelDialogResult | undefined> {
    const data: ModelDialogData = {
      model,
      providers: this.state.llmProviders(),
      initialProviderId,
    };
    return firstValueFrom(
      this.dialog
        .open<ModelDialogComponent, ModelDialogData, ModelDialogResult>(ModelDialogComponent, {
          data,
          width: '600px',
        })
        .afterClosed(),
    );
  }

  private confirm(data: ConfirmDialogData): Promise<boolean | undefined> {
    return firstValueFrom(
      this.dialog
        .open<ConfirmDialogComponent, ConfirmDialogData, boolean>(ConfirmDialogComponent, {
          data,
          width: '440px',
        })
        .afterClosed(),
    );
  }

  private showError(error: unknown): void {
    this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
  }

  private async loadStorageSettings(): Promise<void> {
    this.loadingStorageSettings.set(true);
    try {
      const settings = await this.api.getStorageSettings();
      this.storageSettings.set(settings);
      this.retentionDays.set(settings.retentionDays);
    } catch (error) {
      this.showError(error);
    } finally {
      this.loadingStorageSettings.set(false);
    }
  }

  private async saveMcpDefaults(
    server: McpServer,
    enabled: boolean,
    confirmationMode: ToolConfirmationMode,
    toolPermissions: McpToolPermission[],
  ): Promise<void> {
    if (this.savingMcpDefaultIds().has(server.id)) {
      return;
    }
    this.savingMcpDefaultIds.update((ids) => new Set(ids).add(server.id));
    try {
      const updated = await this.api.updateMcpServerDefaults(server.id, {
        enabled,
        confirmationMode,
        toolPermissions,
      });
      this.state.mcpServers.update((servers) =>
        servers.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (error) {
      this.showError(error);
    } finally {
      this.savingMcpDefaultIds.update((ids) => {
        const next = new Set(ids);
        next.delete(server.id);
        return next;
      });
    }
  }

  private async refreshOAuthStatus(server: McpServer): Promise<void> {
    try {
      this.setOAuthStatus(server.id, await this.api.getMcpOAuthStatus(server.id));
    } catch (error) {
      this.showError(error);
    }
  }

  private async pollOAuthStatus(server: McpServer): Promise<void> {
    for (let attempt = 0; attempt < 600 && !this.destroyed; attempt++) {
      await delay(1000);
      const status = await this.api.getMcpOAuthStatus(server.id);
      this.setOAuthStatus(server.id, status);
      if (
        status.state === 'connected' ||
        status.state === 'error' ||
        status.state === 'disconnected'
      ) {
        return;
      }
    }
  }

  private setOAuthStatus(id: string, status: McpOAuthStatus): void {
    this.oauthStatuses.update((statuses) => ({ ...statuses, [id]: status }));
  }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}
