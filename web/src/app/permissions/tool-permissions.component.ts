import { Component, computed, effect, inject, signal } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormField, form, submit } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSelectModule } from '@angular/material/select';
import { MatSlideToggleModule } from '@angular/material/slide-toggle';
import { MatSnackBar } from '@angular/material/snack-bar';
import { MatTableModule } from '@angular/material/table';
import { MatTooltipModule } from '@angular/material/tooltip';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { firstValueFrom, map } from 'rxjs';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import {
  FilesystemScope,
  McpServerAssignment,
  McpToolPermission,
  ToolConfirmationMode,
  ToolDefinition,
  ToolPermission,
  ToolPermissionSet,
  ToolTargetRule,
} from '../core/models';
import {
  McpToolPermissionsDialogComponent,
  McpToolPermissionsDialogData,
} from './mcp-tool-permissions-dialog.component';
import { TargetRulesDialogComponent, TargetRulesDialogData } from './target-rules-dialog.component';

type PermissionOwnerType = 'workspace' | 'session';

interface PermissionRow {
  definition: ToolDefinition;
  index: number;
}

@Component({
  selector: 'app-tool-permissions',
  imports: [
    FormField,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatProgressBarModule,
    MatProgressSpinnerModule,
    MatSelectModule,
    MatSlideToggleModule,
    MatTableModule,
    MatTooltipModule,
    RouterLink,
  ],
  templateUrl: './tool-permissions.component.html',
  styleUrl: './tool-permissions.component.scss',
})
export class ToolPermissionsComponent {
  private readonly api = inject(ApiService);
  private readonly state = inject(AppState);
  private readonly dialog = inject(MatDialog);
  private readonly route = inject(ActivatedRoute);
  private readonly snackBar = inject(MatSnackBar);
  protected readonly ownerType = this.route.snapshot.data['ownerType'] as PermissionOwnerType;
  private readonly ownerId = toSignal(
    this.route.paramMap.pipe(
      map((params) => params.get(this.ownerType === 'workspace' ? 'workspaceId' : 'sessionId')),
    ),
    {
      initialValue: this.route.snapshot.paramMap.get(
        this.ownerType === 'workspace' ? 'workspaceId' : 'sessionId',
      ),
    },
  );
  protected readonly loading = signal(true);
  protected readonly saving = signal(false);
  protected readonly permissionSet = signal<ToolPermissionSet | null>(null);
  protected readonly mcpAssignments = signal<McpServerAssignment[]>([]);
  private readonly model = signal({ permissions: [] as ToolPermission[] });
  protected readonly permissionsForm = form(this.model);
  private readonly savedPermissionValue = signal('');
  private readonly savedMcpValue = signal('');
  protected readonly displayedColumns = ['tool', 'confirmation', 'access', 'rules'];
  protected readonly mcpDisplayedColumns = ['server', 'enabled', 'confirmation', 'overrides'];
  protected readonly rows = computed<PermissionRow[]>(() => {
    const permissionSet = this.permissionSet();
    if (!permissionSet) {
      return [];
    }
    return permissionSet.definitions.map((definition, index) => ({ definition, index }));
  });
  private readonly toolPermissionChanges = computed(
    () => JSON.stringify(this.model().permissions) !== this.savedPermissionValue(),
  );
  private readonly mcpChanges = computed(
    () => JSON.stringify(this.mcpAssignments()) !== this.savedMcpValue(),
  );
  protected readonly hasChanges = computed(() => this.toolPermissionChanges() || this.mcpChanges());
  protected readonly hasComputerAccess = computed(() =>
    this.model().permissions.some((permission) => permission.filesystemScope === 'computer'),
  );
  protected readonly runCommandPermission = computed(() =>
    this.model().permissions.find((permission) => permission.toolName === 'run_command'),
  );
  protected readonly sessionActive = computed(() => {
    if (this.ownerType !== 'session') {
      return false;
    }
    const session = this.state.allSessions().find((item) => item.id === this.ownerId());
    if (session) {
      return session.status !== 'idle';
    }
    const sessionStatus = this.permissionSet()?.sessionStatus;
    return sessionStatus !== undefined && sessionStatus !== 'idle';
  });
  protected readonly mcpConfigurationLocked = computed(() => {
    if (this.ownerType !== 'session') {
      return false;
    }
    const session = this.state.allSessions().find((item) => item.id === this.ownerId());
    return session?.runtimeType === 'acp' && !!session.acpSessionId;
  });
  protected readonly backLink = computed(() => {
    const permissionSet = this.permissionSet();
    if (!permissionSet) {
      return ['/'];
    }
    return this.ownerType === 'workspace'
      ? ['/workspace', permissionSet.workspaceId]
      : ['/session', permissionSet.ownerId];
  });
  private loadGeneration = 0;

  constructor() {
    effect(() => {
      const ownerId = this.ownerId();
      if (ownerId) {
        void this.load(ownerId);
      }
    });
  }

  protected iconFor(toolName: string): string {
    switch (toolName) {
      case 'list_directory':
        return 'folder_open';
      case 'read_file':
        return 'description';
      case 'grep':
        return 'search';
      case 'fetch_url':
        return 'language';
      case 'edit_file':
        return 'edit_note';
      case 'load_skill':
        return 'auto_stories';
      case 'run_command':
        return 'terminal';
      default:
        return 'build';
    }
  }

  protected scopeLabel(scope: FilesystemScope, toolName: string): string {
    if (toolName === 'run_command') {
      return scope === 'repository' ? 'Repository directories' : 'Workspace directories';
    }
    switch (scope) {
      case 'workspace':
        return 'Workspace only';
      case 'repository':
        return 'Repository';
      case 'computer':
        return 'All files on this computer';
      default:
        return 'Not applicable';
    }
  }

  protected scopePath(scope: FilesystemScope): string {
    const permissionSet = this.permissionSet();
    if (!permissionSet) {
      return '';
    }
    switch (scope) {
      case 'workspace':
        return permissionSet.workspaceRoot;
      case 'repository':
        return permissionSet.repositoryRoot ?? 'Repository root unavailable';
      case 'computer':
        return 'Limited by operating-system access';
      default:
        return 'Public HTTP(S)';
    }
  }

  protected fixedAccessIcon(toolName: string): string {
    return toolName === 'fetch_url' ? 'public' : 'auto_stories';
  }

  protected fixedAccessLabel(toolName: string): string {
    return toolName === 'fetch_url' ? 'Public HTTP(S)' : 'Workspace, parent, and global skills';
  }

  protected ruleCount(index: number): number {
    return this.model().permissions[index]?.targetRules.length ?? 0;
  }

  protected setMcpEnabled(index: number, enabled: boolean): void {
    if (this.mcpConfigurationLocked()) {
      return;
    }
    this.updateMcpAssignment(index, (assignment) => ({ ...assignment, enabled }));
  }

  protected setMcpConfirmation(index: number, confirmationMode: ToolConfirmationMode): void {
    if (this.mcpConfigurationLocked()) {
      return;
    }
    this.updateMcpAssignment(index, (assignment) => ({
      ...assignment,
      confirmationMode,
    }));
  }

  protected mcpOverrideCount(assignment: McpServerAssignment): number {
    return assignment.toolPermissions?.length ?? 0;
  }

  protected mcpConnection(assignment: McpServerAssignment): string {
    const server = assignment.server;
    return server.transport === 'stdio'
      ? [server.command, ...server.arguments].join(' ')
      : server.url;
  }

  protected mcpTransportIcon(assignment: McpServerAssignment): string {
    return assignment.server.transport === 'stdio' ? 'terminal' : 'language';
  }

  protected mcpAvailabilityLabel(assignment: McpServerAssignment): string {
    const server = assignment.server;
    if (!server.available) {
      return server.credentialAvailable
        ? 'Command unavailable'
        : 'Required environment variable is not set';
    }
    return server.authType === 'oauth' ? 'OAuth managed in Settings' : 'Ready';
  }

  protected async editMcpToolPermissions(
    assignment: McpServerAssignment,
    index: number,
  ): Promise<void> {
    if (this.mcpConfigurationLocked()) {
      return;
    }
    try {
      const catalog = await this.api.listMcpServerTools(assignment.server.id);
      const result = await firstValueFrom(
        this.dialog
          .open<
            McpToolPermissionsDialogComponent,
            McpToolPermissionsDialogData,
            McpToolPermission[]
          >(McpToolPermissionsDialogComponent, {
            data: {
              serverName: assignment.server.name,
              defaultConfirmation: assignment.confirmationMode,
              tools: catalog.tools,
              permissions: assignment.toolPermissions ?? [],
            },
            width: '820px',
            maxWidth: '96vw',
          })
          .afterClosed(),
      );
      if (!result) {
        return;
      }
      this.updateMcpAssignment(index, (current) => ({
        ...current,
        toolPermissions: result,
      }));
    } catch (error) {
      this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
    }
  }

  protected async editTargetRules(row: PermissionRow): Promise<void> {
    const permission = this.model().permissions[row.index];
    if (!permission) {
      return;
    }
    const rules = await firstValueFrom(
      this.dialog
        .open<TargetRulesDialogComponent, TargetRulesDialogData, ToolTargetRule[]>(
          TargetRulesDialogComponent,
          {
            data: {
              toolLabel: row.definition.label,
              supportedMatchers: row.definition.supportedTargetMatchers,
              rules: permission.targetRules.map((rule) => ({ ...rule })),
            },
            width: '960px',
            maxWidth: '96vw',
          },
        )
        .afterClosed(),
    );
    if (!rules) {
      return;
    }
    this.model.update((current) => ({
      permissions: current.permissions.map((item, index) =>
        index === row.index ? { ...item, targetRules: rules } : item,
      ),
    }));
  }

  protected save(event: SubmitEvent): void {
    event.preventDefault();
    void submit(this.permissionsForm, async () => {
      const ownerId = this.ownerId();
      if (!ownerId || !this.hasChanges()) {
        return;
      }
      this.saving.set(true);
      try {
        if (this.toolPermissionChanges()) {
          const permissions = clonePermissions(this.model().permissions);
          const response =
            this.ownerType === 'workspace'
              ? await this.api.replaceWorkspaceToolPermissions(ownerId, permissions)
              : await this.api.replaceSessionToolPermissions(ownerId, permissions);
          this.applyPermissionResponse(response);
        }
        if (this.mcpChanges()) {
          const assignments = cloneMcpAssignments(this.mcpAssignments());
          const response =
            this.ownerType === 'workspace'
              ? await this.api.replaceWorkspaceMcpServers(ownerId, assignments)
              : await this.api.replaceSessionMcpServers(ownerId, assignments);
          this.applyMcpResponse(response);
        }
        this.snackBar.open('Permissions saved', undefined, { duration: 3000 });
      } catch (error) {
        this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
      } finally {
        this.saving.set(false);
      }
    });
  }

  private async load(ownerId: string): Promise<void> {
    const generation = ++this.loadGeneration;
    this.loading.set(true);
    try {
      const [response, mcpAssignments] = await Promise.all([
        this.ownerType === 'workspace'
          ? this.api.getWorkspaceToolPermissions(ownerId)
          : this.api.getSessionToolPermissions(ownerId),
        this.ownerType === 'workspace'
          ? this.api.getWorkspaceMcpServers(ownerId)
          : this.api.getSessionMcpServers(ownerId),
      ]);
      if (generation !== this.loadGeneration) {
        return;
      }
      this.applyPermissionResponse(response);
      this.applyMcpResponse(mcpAssignments);
      await this.state.selectWorkspace(response.workspaceId);
    } catch (error) {
      if (generation === this.loadGeneration) {
        this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
      }
    } finally {
      if (generation === this.loadGeneration) {
        this.loading.set(false);
      }
    }
  }

  private applyPermissionResponse(response: ToolPermissionSet): void {
    const permissions = clonePermissions(response.permissions ?? []);
    this.permissionSet.set({
      ...response,
      definitions: (response.definitions ?? []).map((definition) => ({
        ...definition,
        supportedScopes: definition.supportedScopes ?? [],
        supportedTargetMatchers: definition.supportedTargetMatchers ?? [],
      })),
      permissions,
    });
    this.model.set({ permissions });
    this.savedPermissionValue.set(JSON.stringify(permissions));
  }

  private applyMcpResponse(assignments: McpServerAssignment[]): void {
    const normalized = cloneMcpAssignments(assignments ?? []);
    this.mcpAssignments.set(normalized);
    this.savedMcpValue.set(JSON.stringify(normalized));
  }

  private updateMcpAssignment(
    index: number,
    update: (assignment: McpServerAssignment) => McpServerAssignment,
  ): void {
    this.mcpAssignments.update((assignments) =>
      assignments.map((assignment, currentIndex) =>
        currentIndex === index ? update(assignment) : assignment,
      ),
    );
  }
}

function clonePermissions(permissions: ToolPermission[]): ToolPermission[] {
  return permissions.map((permission) => ({
    ...permission,
    targetRules: permission.targetRules.map((rule) => ({ ...rule })),
  }));
}

function cloneMcpAssignments(assignments: McpServerAssignment[]): McpServerAssignment[] {
  return assignments.map((assignment) => ({
    ...assignment,
    server: {
      ...assignment.server,
      arguments: [...(assignment.server.arguments ?? [])],
      environment: (assignment.server.environment ?? []).map((binding) => ({ ...binding })),
      headers: (assignment.server.headers ?? []).map((binding) => ({ ...binding })),
      oauthScopes: [...(assignment.server.oauthScopes ?? [])],
    },
    toolPermissions: (assignment.toolPermissions ?? []).map((permission) => ({ ...permission })),
  }));
}
