import { BreakpointObserver } from '@angular/cdk/layout';
import { Component, OnInit, computed, effect, inject, viewChild } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatIconModule } from '@angular/material/icon';
import { MatMenuModule } from '@angular/material/menu';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSidenav, MatSidenavModule } from '@angular/material/sidenav';
import { MatSnackBar } from '@angular/material/snack-bar';
import { MatToolbarModule } from '@angular/material/toolbar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { NavigationEnd, Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { filter, firstValueFrom, map, startWith } from 'rxjs';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import { AppSession, Workspace } from '../core/models';
import { ConfirmDialogComponent, ConfirmDialogData } from '../shared/confirm-dialog.component';
import {
  SessionDialogComponent,
  SessionDialogData,
  SessionDialogResult,
} from '../shared/session-dialog.component';
import {
  WorkspaceDialogComponent,
  WorkspaceDialogData,
  WorkspaceDialogResult,
} from '../shared/workspace-dialog.component';

interface WorkspaceGroup {
  workspace: Workspace;
  sessions: AppSession[];
}

@Component({
  selector: 'app-shell',
  imports: [
    MatButtonModule,
    MatIconModule,
    MatMenuModule,
    MatProgressBarModule,
    MatProgressSpinnerModule,
    MatSidenavModule,
    MatToolbarModule,
    MatTooltipModule,
    RouterLink,
    RouterLinkActive,
    RouterOutlet,
  ],
  templateUrl: './shell.component.html',
  styleUrl: './shell.component.scss',
})
export class ShellComponent implements OnInit {
  protected readonly state = inject(AppState);
  private readonly api = inject(ApiService);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);
  private readonly router = inject(Router);
  private readonly breakpointObserver = inject(BreakpointObserver);
  private readonly sidenav = viewChild(MatSidenav);
  private readonly routeUrl = toSignal(
    this.router.events.pipe(
      filter((event): event is NavigationEnd => event instanceof NavigationEnd),
      map((event) => event.urlAfterRedirects),
      startWith(this.router.url),
    ),
    { initialValue: this.router.url },
  );

  protected readonly mobile = toSignal(
    this.breakpointObserver.observe('(max-width: 840px)').pipe(map((result) => result.matches)),
    { initialValue: false },
  );
  protected readonly llmProvidersReady = computed(() => {
    const providers = this.state.llmProviders();
    return providers.length > 0 && providers.every((item) => item.credentialAvailable);
  });
  protected readonly agentRuntimeReady = computed(
    () => this.llmProvidersReady() || this.state.acpAgents().some((agent) => agent.available),
  );
  protected readonly llmStatusLabel = computed(() => {
    const availableACPAgents = this.state.acpAgents().filter((agent) => agent.available);
    const providers = this.state.llmProviders();
    if (providers.length === 0 && availableACPAgents.length === 0) {
      return 'No agent runtime is ready';
    }
    if (this.llmProvidersReady() || availableACPAgents.length > 0) {
      return 'Agent runtime ready';
    }
    return 'One or more provider credentials are unavailable';
  });
  protected readonly workspaceGroups = computed<WorkspaceGroup[]>(() => {
    const sessions = this.state.allSessions();
    return this.state.workspaces().map((workspace) => ({
      workspace,
      sessions: sessions.filter((session) => session.workspaceId === workspace.id),
    }));
  });
  protected readonly activeSession = computed(() => {
    const segments = this.router.parseUrl(this.routeUrl()).root.children['primary']?.segments;
    const sessionId = segments?.[0]?.path === 'session' ? segments[1]?.path : null;
    return this.state.allSessions().find((session) => session.id === sessionId) ?? null;
  });
  protected readonly activeSessionWorkspace = computed(() => {
    const session = this.activeSession();
    return (
      this.state.workspaces().find((workspace) => workspace.id === session?.workspaceId) ?? null
    );
  });
  protected readonly settingsActive = computed(() => this.routeUrl().startsWith('/settings'));
  private readonly hasActiveSessions = computed(() =>
    this.state.allSessions().some((session) => session.status !== 'idle'),
  );

  constructor() {
    effect((onCleanup) => {
      if (!this.hasActiveSessions()) {
        return;
      }

      const timer = window.setInterval(() => {
        void this.state.refreshSessions().catch(() => undefined);
      }, 1500);
      onCleanup(() => window.clearInterval(timer));
    });
  }

  async ngOnInit(): Promise<void> {
    try {
      await this.state.initialize();
    } catch (error) {
      this.showError(error);
    }
  }

  protected toggleNavigation(): void {
    this.sidenav()?.toggle();
  }

  protected async selectWorkspace(workspace: Workspace): Promise<void> {
    try {
      if (workspace.id !== this.state.selectedWorkspaceId()) {
        await this.state.selectWorkspace(workspace.id);
      }
      await this.router.navigate(['/workspace', workspace.id]);
      this.closeMobileNavigation();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async addWorkspace(): Promise<void> {
    const result = await firstValueFrom(
      this.dialog
        .open<WorkspaceDialogComponent, WorkspaceDialogData, WorkspaceDialogResult>(
          WorkspaceDialogComponent,
          { data: { mode: 'create', name: '', rootPath: '' }, width: '500px' },
        )
        .afterClosed(),
    );
    if (!result) {
      return;
    }
    try {
      const workspace = await this.api.createWorkspace(result);
      await this.state.refreshWorkspaces();
      await this.state.selectWorkspace(workspace.id);
      await this.router.navigate(['/workspace', workspace.id]);
    } catch (error) {
      this.showError(error);
    }
  }

  protected async renameWorkspace(workspace: Workspace): Promise<void> {
    const result = await firstValueFrom(
      this.dialog
        .open<WorkspaceDialogComponent, WorkspaceDialogData, WorkspaceDialogResult>(
          WorkspaceDialogComponent,
          {
            data: {
              mode: 'rename',
              name: workspace.name,
              rootPath: workspace.rootPath,
            },
            width: '500px',
          },
        )
        .afterClosed(),
    );
    if (!result) {
      return;
    }
    try {
      await this.api.updateWorkspace(workspace.id, result.name);
      await this.state.refreshWorkspaces();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async deleteWorkspace(workspace: Workspace): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Remove workspace',
      message: `Remove "${workspace.name}" from MaterialMind? Files on disk are not changed.`,
      confirmLabel: 'Remove',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteWorkspace(workspace.id);
      await Promise.all([this.state.refreshWorkspaces(), this.state.refreshSessions()]);
      if (this.state.selectedWorkspaceId() === workspace.id) {
        const nextWorkspace = this.state.workspaces()[0];
        await this.state.selectWorkspace(nextWorkspace?.id ?? null);
        await this.router.navigate(nextWorkspace ? ['/workspace', nextWorkspace.id] : ['/'], {
          replaceUrl: true,
        });
      }
    } catch (error) {
      this.showError(error);
    }
  }

  protected async addSession(workspace: Workspace): Promise<void> {
    const models = this.state.llmModels();
    const acpAgents = this.state.acpAgents();
    if (models.length === 0 && !acpAgents.some((agent) => agent.available)) {
      this.snackBar.open('Configure an agent runtime first.', 'Open settings', {
        duration: 6000,
      });
      await this.router.navigate(['/settings']);
      this.closeMobileNavigation();
      return;
    }
    const result = await firstValueFrom(
      this.dialog
        .open<SessionDialogComponent, SessionDialogData, SessionDialogResult>(
          SessionDialogComponent,
          {
            data: {
              title: '',
              runtimeType: models.length > 0 ? 'adk' : 'acp',
              selectedLlmModelId: models[0]?.id ?? '',
              acpAgentId: acpAgents.find((agent) => agent.available)?.id ?? '',
              models,
              providers: this.state.llmProviders(),
              acpAgents,
            },
            width: '500px',
          },
        )
        .afterClosed(),
    );
    if (!result) {
      return;
    }
    try {
      const session = await this.api.createSession({
        workspaceId: workspace.id,
        title: result.title,
        runtimeType: result.runtimeType,
        llmModelId: result.llmModelId,
        acpAgentId: result.acpAgentId,
      });
      await Promise.all([this.state.refreshSessions(), this.state.selectWorkspace(workspace.id)]);
      await this.router.navigate(['/session', session.id]);
      this.closeMobileNavigation();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async editSession(session: AppSession): Promise<void> {
    const models = this.state.llmModels();
    const result = await firstValueFrom(
      this.dialog
        .open<SessionDialogComponent, SessionDialogData, SessionDialogResult>(
          SessionDialogComponent,
          {
            data: {
              title: session.title,
              runtimeType: session.runtimeType,
              selectedLlmModelId: session.selectedLlmModelId ?? models[0]?.id ?? '',
              acpAgentId: session.acpAgentId ?? '',
              models,
              providers: this.state.llmProviders(),
              acpAgents: this.state.acpAgents(),
            },
            width: '500px',
          },
        )
        .afterClosed(),
    );
    if (!result) {
      return;
    }
    try {
      await this.api.updateSession(session.id, {
        title: result.title,
        llmModelId: result.llmModelId,
      });
      await this.state.refreshSessions();
    } catch (error) {
      this.showError(error);
    }
  }

  protected async deleteSession(session: AppSession): Promise<void> {
    const confirmed = await this.confirm({
      title: 'Delete session',
      message: `Delete "${session.title}" and its conversation history?`,
      confirmLabel: 'Delete',
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.api.deleteSession(session.id);
      await this.state.refreshSessions();
      if (this.router.url.includes(session.id)) {
        await this.router.navigate(['/workspace', session.workspaceId]);
      }
    } catch (error) {
      this.showError(error);
    }
  }

  protected closeMobileNavigation(): void {
    if (this.mobile()) {
      this.sidenav()?.close();
    }
  }

  protected prepareSession(session: AppSession): void {
    void this.state.selectWorkspace(session.workspaceId);
    this.closeMobileNavigation();
  }

  protected prepareWorkspace(workspace: Workspace): void {
    void this.state.selectWorkspace(workspace.id);
    this.closeMobileNavigation();
  }

  private async confirm(data: ConfirmDialogData): Promise<boolean> {
    return (
      (await firstValueFrom(
        this.dialog
          .open<ConfirmDialogComponent, ConfirmDialogData, boolean>(ConfirmDialogComponent, {
            data,
            width: '420px',
          })
          .afterClosed(),
      )) ?? false
    );
  }

  private showError(error: unknown): void {
    this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
  }
}
