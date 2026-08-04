import { Component, computed, effect, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatIconModule } from '@angular/material/icon';
import { MatSnackBar } from '@angular/material/snack-bar';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { firstValueFrom, map } from 'rxjs';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
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

@Component({
  selector: 'app-overview',
  imports: [MatButtonModule, MatIconModule, RouterLink],
  template: `
    <main class="overview-page">
      @if (state.selectedWorkspace(); as workspace) {
        <header>
          <div class="workspace-icon"><mat-icon>folder_open</mat-icon></div>
          <div>
            <h1>{{ workspace.name }}</h1>
            <p>{{ workspace.rootPath }}</p>
          </div>
          <span class="availability" [class.available]="workspace.available">
            {{ workspace.available ? 'Available' : 'Unavailable' }}
          </span>
        </header>
        <section class="workspace-summary" aria-label="Workspace summary">
          <div>
            <strong>{{ state.sessions().length }}</strong
            ><span>Sessions</span>
          </div>
          <div>
            <strong>{{ state.llmModels().length }}</strong
            ><span>Models</span>
          </div>
          <div>
            <strong>{{ runningSessions() }}</strong
            ><span>Running</span>
          </div>
        </section>
        <div class="actions">
          <button mat-flat-button type="button" (click)="createSession()">
            <mat-icon>add_comment</mat-icon>
            New session
          </button>
          <a mat-button [routerLink]="['/workspace', workspace.id, 'permissions']">
            <mat-icon>admin_panel_settings</mat-icon>
            Permissions
          </a>
          @if (!hasReadyRuntime()) {
            <a mat-button routerLink="/settings">Configure runtime</a>
          }
        </div>
      } @else {
        <div class="no-workspace">
          <mat-icon>create_new_folder</mat-icon>
          <h1>No workspace</h1>
          <button mat-flat-button type="button" (click)="addWorkspace()">Add workspace</button>
        </div>
      }
    </main>
  `,
  styles: `
    :host {
      display: block;
      height: 100%;
      overflow: auto;
    }

    .overview-page {
      width: min(820px, calc(100% - 48px));
      margin: 0 auto;
      padding: 48px 0;
    }

    header {
      display: grid;
      grid-template-columns: 48px minmax(0, 1fr) auto;
      align-items: center;
      gap: 16px;
    }

    .workspace-icon {
      display: grid;
      width: 48px;
      height: 48px;
      place-items: center;
      border-radius: 8px;
      background: var(--mat-sys-primary-container);
      color: var(--mat-sys-on-primary-container);
    }

    h1,
    p {
      margin: 0;
    }

    h1 {
      font: var(--mat-sys-headline-small);
    }

    p {
      color: var(--mat-sys-on-surface-variant);
      overflow-wrap: anywhere;
    }

    .availability {
      border: 1px solid var(--mat-sys-error);
      border-radius: 4px;
      color: var(--mat-sys-error);
      font-size: 12px;
      padding: 4px 8px;
    }

    .availability.available {
      border-color: var(--mat-sys-primary);
      color: var(--mat-sys-primary);
    }

    .workspace-summary {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      margin-top: 40px;
      border-block: 1px solid var(--mat-sys-outline-variant);
      padding: 20px 0;
    }

    .workspace-summary > div {
      display: grid;
      gap: 4px;
      border-right: 1px solid var(--mat-sys-outline-variant);
      padding-left: 20px;
    }

    .workspace-summary > div:first-child {
      padding-left: 0;
    }

    .workspace-summary > div:last-child {
      border-right: 0;
    }

    .workspace-summary strong {
      font: var(--mat-sys-headline-small);
    }

    .workspace-summary span {
      color: var(--mat-sys-on-surface-variant);
      font-size: 12px;
    }

    .actions {
      display: flex;
      gap: 8px;
      margin-top: 24px;
    }

    .no-workspace {
      display: grid;
      min-height: 60vh;
      place-content: center;
      justify-items: center;
      gap: 12px;
      color: var(--mat-sys-on-surface-variant);
    }

    .no-workspace > mat-icon {
      width: 42px;
      height: 42px;
      color: var(--mat-sys-primary);
      font-size: 42px;
    }

    @media (max-width: 600px) {
      .overview-page {
        width: calc(100% - 28px);
        padding-top: 28px;
      }

      header {
        grid-template-columns: 42px minmax(0, 1fr);
      }

      .availability {
        grid-column: 2;
        justify-self: start;
      }
    }
  `,
})
export class OverviewComponent {
  protected readonly state = inject(AppState);
  protected readonly runningSessions = computed(
    () => this.state.sessions().filter((session) => session.status !== 'idle').length,
  );
  protected readonly hasReadyRuntime = computed(
    () =>
      this.state.llmModels().length > 0 || this.state.acpAgents().some((agent) => agent.available),
  );
  private readonly api = inject(ApiService);
  private readonly dialog = inject(MatDialog);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly snackBar = inject(MatSnackBar);
  private readonly routeWorkspaceId = toSignal(
    this.route.paramMap.pipe(map((params) => params.get('workspaceId'))),
    { initialValue: this.route.snapshot.paramMap.get('workspaceId') },
  );

  constructor() {
    effect(() => {
      const workspaceId = this.routeWorkspaceId();
      if (!workspaceId) {
        return;
      }

      if (this.state.loading()) {
        if (workspaceId !== this.state.selectedWorkspaceId()) {
          void this.state.selectWorkspace(workspaceId);
        }
        return;
      }

      if (!this.state.workspaces().some((workspace) => workspace.id === workspaceId)) {
        const fallback = this.state.selectedWorkspace();
        void this.router.navigate(fallback ? ['/workspace', fallback.id] : ['/'], {
          replaceUrl: true,
        });
        return;
      }

      if (workspaceId !== this.state.selectedWorkspaceId()) {
        void this.state.selectWorkspace(workspaceId);
      }
    });
  }

  protected async createSession(): Promise<void> {
    const workspace = this.state.selectedWorkspace();
    if (!workspace) {
      return;
    }
    const models = this.state.llmModels();
    const acpAgents = this.state.acpAgents();
    if (models.length === 0 && !acpAgents.some((agent) => agent.available)) {
      await this.router.navigate(['/settings']);
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
      await this.state.refreshSessions();
      await this.router.navigate(['/session', session.id]);
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

  private showError(error: unknown): void {
    this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
  }
}
