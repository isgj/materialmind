import { DatePipe } from '@angular/common';
import { Component, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import {
  MAT_DIALOG_DATA,
  MatDialogContent,
  MatDialogModule,
  MatDialogRef,
} from '@angular/material/dialog';
import { MatDividerModule } from '@angular/material/divider';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSelectModule } from '@angular/material/select';
import { MatSnackBar } from '@angular/material/snack-bar';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import {
  AcpAgent,
  AcpAgentAuthMethod,
  AcpAgentInspection,
  AcpRemoteSession,
  Workspace,
} from '../core/models';

export interface AcpCapabilitiesDialogData {
  agent: AcpAgent;
  workspaces: Workspace[];
}

@Component({
  selector: 'app-acp-capabilities-dialog',
  imports: [
    DatePipe,
    MatButtonModule,
    MatDialogContent,
    MatDialogModule,
    MatDividerModule,
    MatFormFieldModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatSelectModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.agent.name }}</h2>
    <mat-dialog-content>
      @if (loading()) {
        <div class="loading-state" role="status">
          <mat-spinner diameter="28" />
          <span>Inspecting ACP capabilities</span>
        </div>
      } @else if (inspection(); as details) {
        <section class="summary" aria-label="Runtime capabilities">
          <div>
            <mat-icon aria-hidden="true">handshake</mat-icon>
            <span>Protocol {{ details.protocolVersion }}</span>
          </div>
          @if (details.implementation; as implementation) {
            <div>
              <mat-icon aria-hidden="true">deployed_code</mat-icon>
              <span>
                {{ implementation.title || implementation.name }} {{ implementation.version }}
              </span>
            </div>
          }
          <div>
            <mat-icon aria-hidden="true">folder_managed</mat-icon>
            <span>{{ restoreLabel(details) }}</span>
          </div>
        </section>

        @if (details.authMethods.length > 0 || details.logout) {
          <mat-divider />
          <section class="dialog-section" aria-labelledby="acp-auth-heading">
            <div class="section-heading">
              <h3 id="acp-auth-heading">Authentication</h3>
              @if (details.logout) {
                <button mat-button type="button" [disabled]="logoutLoading()" (click)="logout()">
                  <mat-icon>logout</mat-icon>
                  {{ logoutLoading() ? 'Signing out' : 'Sign out' }}
                </button>
              }
            </div>
            <div class="item-list">
              @for (method of details.authMethods; track method.id) {
                <div class="auth-item">
                  <div>
                    <strong>{{ method.name }}</strong>
                    <span>{{ method.description || authTypeLabel(method) }}</span>
                    @if (!method.supported) {
                      <span class="unsupported">{{ unsupportedAuthLabel(method) }}</span>
                    }
                  </div>
                  <button
                    mat-stroked-button
                    type="button"
                    [disabled]="!method.supported || authenticatingIds().has(method.id)"
                    (click)="authenticate(method)"
                  >
                    <mat-icon>login</mat-icon>
                    {{ authenticatingIds().has(method.id) ? 'Authenticating' : 'Authenticate' }}
                  </button>
                </div>
              }
            </div>
          </section>
        }

        <mat-divider />
        <section class="dialog-section" aria-labelledby="acp-sessions-heading">
          <div class="section-heading">
            <div>
              <h3 id="acp-sessions-heading">Agent sessions</h3>
              <p>Import a session into a workspace with the same working directory.</p>
            </div>
            @if (details.sessions.list) {
              <button
                mat-icon-button
                type="button"
                aria-label="Refresh ACP sessions"
                [disabled]="sessionsLoading()"
                (click)="loadSessions()"
              >
                <mat-icon>refresh</mat-icon>
              </button>
            }
          </div>

          @if (!details.sessions.list) {
            <div class="empty-state">This agent does not advertise session discovery.</div>
          } @else if (sessionsLoading()) {
            <div class="loading-state compact" role="status">
              <mat-spinner diameter="22" />
              <span>Loading sessions</span>
            </div>
          } @else if (sessions().length === 0) {
            <div class="empty-state">No sessions were reported by the agent.</div>
          } @else {
            <div class="session-list">
              @for (session of sessions(); track session.id) {
                <div class="session-item">
                  <div class="session-copy">
                    <strong>{{ session.title || 'Untitled session' }}</strong>
                    <code>{{ session.workingDirectory }}</code>
                    @if (session.updatedAt) {
                      <span>Updated {{ session.updatedAt | date: 'medium' }}</span>
                    }
                  </div>
                  <mat-form-field appearance="outline" subscriptSizing="dynamic">
                    <mat-label>Workspace</mat-label>
                    <mat-select
                      [value]="selectedWorkspace(session)"
                      (selectionChange)="selectWorkspace(session.id, $event.value)"
                    >
                      @for (workspace of compatibleWorkspaces(session); track workspace.id) {
                        <mat-option [value]="workspace.id">{{ workspace.name }}</mat-option>
                      }
                    </mat-select>
                  </mat-form-field>
                  <button
                    mat-flat-button
                    type="button"
                    [disabled]="
                      !restoreSupported() ||
                      !selectedWorkspace(session) ||
                      importingIds().has(session.id) ||
                      importedIds().has(session.id)
                    "
                    (click)="importSession(session)"
                  >
                    <mat-icon>{{ importedIds().has(session.id) ? 'check' : 'download' }}</mat-icon>
                    {{ importedIds().has(session.id) ? 'Imported' : 'Import' }}
                  </button>
                </div>
              }
            </div>
          }
        </section>
      }

      @if (error()) {
        <div class="error-state" role="alert">
          <mat-icon aria-hidden="true">error_outline</mat-icon>
          <span>{{ error() }}</span>
        </div>
      }
    </mat-dialog-content>
    <div mat-dialog-actions align="end">
      <button mat-button type="button" (click)="dialogRef.close()">Close</button>
    </div>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      gap: 20px;
      min-height: 180px;
      padding-top: 8px;
    }

    h3,
    p {
      margin: 0;
    }

    h3 {
      font: var(--mat-sys-title-medium);
    }

    .summary {
      display: flex;
      flex-wrap: wrap;
      gap: 12px 24px;
    }

    .summary div,
    .loading-state,
    .error-state {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .summary div {
      color: var(--mat-sys-on-surface-variant);
    }

    .dialog-section,
    .item-list,
    .session-list,
    .session-copy {
      display: grid;
    }

    .dialog-section,
    .item-list,
    .session-list {
      gap: 12px;
    }

    .section-heading,
    .auth-item,
    .session-item {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
    }

    .section-heading p,
    .auth-item span,
    .session-copy span,
    .empty-state {
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
    }

    .auth-item > div {
      display: grid;
      min-width: 0;
      gap: 2px;
    }

    .unsupported {
      color: var(--mat-sys-error) !important;
    }

    .session-list {
      max-height: 360px;
      overflow: auto;
    }

    .session-item {
      padding: 12px;
      border: 1px solid var(--mat-sys-outline-variant);
      border-radius: 8px;
    }

    .session-copy {
      min-width: 0;
      flex: 1;
      gap: 3px;
    }

    .session-copy code {
      overflow: hidden;
      color: var(--mat-sys-on-surface-variant);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .session-item mat-form-field {
      width: 190px;
    }

    .loading-state {
      justify-content: center;
      min-height: 120px;
    }

    .loading-state.compact {
      min-height: 72px;
    }

    .error-state {
      padding: 12px;
      border-radius: 6px;
      background: var(--mat-sys-error-container);
      color: var(--mat-sys-on-error-container);
    }

    @media (max-width: 720px) {
      .auth-item,
      .session-item {
        align-items: stretch;
        flex-direction: column;
      }

      .session-item mat-form-field {
        width: 100%;
      }
    }
  `,
})
export class AcpCapabilitiesDialogComponent {
  protected readonly data = inject<AcpCapabilitiesDialogData>(MAT_DIALOG_DATA);
  protected readonly dialogRef = inject(MatDialogRef<AcpCapabilitiesDialogComponent>);
  private readonly api = inject(ApiService);
  private readonly appState = inject(AppState);
  private readonly snackBar = inject(MatSnackBar);

  protected readonly loading = signal(true);
  protected readonly inspection = signal<AcpAgentInspection | null>(null);
  protected readonly sessions = signal<AcpRemoteSession[]>([]);
  protected readonly sessionsLoading = signal(false);
  protected readonly error = signal('');
  protected readonly workspaceSelections = signal<Record<string, string>>({});
  protected readonly authenticatingIds = signal<Set<string>>(new Set());
  protected readonly logoutLoading = signal(false);
  protected readonly importingIds = signal<Set<string>>(new Set());
  protected readonly importedIds = signal<Set<string>>(new Set());
  protected readonly restoreSupported = computed(() => {
    const details = this.inspection();
    return !!details && (details.sessions.load || details.sessions.resume);
  });

  constructor() {
    void this.load();
  }

  protected restoreLabel(details: AcpAgentInspection): string {
    if (details.sessions.resume) {
      return 'Session resume supported';
    }
    if (details.sessions.load) {
      return 'Session load supported';
    }
    return 'Sessions cannot be restored';
  }

  protected authTypeLabel(method: AcpAgentAuthMethod): string {
    switch (method.type) {
      case 'agent':
        return 'Handled by the agent';
      case 'env_var':
        return 'Environment variable credentials';
      case 'terminal':
        return 'Interactive terminal authentication';
    }
  }

  protected unsupportedAuthLabel(method: AcpAgentAuthMethod): string {
    return method.type === 'terminal'
      ? 'Interactive terminal authentication is not available in the web client.'
      : 'This authentication method is not enabled by MaterialMind.';
  }

  protected compatibleWorkspaces(session: AcpRemoteSession): Workspace[] {
    return this.data.workspaces.filter(
      (workspace) => workspace.available && workspace.rootPath === session.workingDirectory,
    );
  }

  protected selectedWorkspace(session: AcpRemoteSession): string {
    return this.workspaceSelections()[session.id] ?? '';
  }

  protected selectWorkspace(sessionId: string, workspaceId: string): void {
    this.workspaceSelections.update((current) => ({ ...current, [sessionId]: workspaceId }));
  }

  protected async authenticate(method: AcpAgentAuthMethod): Promise<void> {
    if (!method.supported || this.authenticatingIds().has(method.id)) {
      return;
    }
    this.authenticatingIds.update((current) => new Set(current).add(method.id));
    this.error.set('');
    try {
      this.inspection.set(await this.api.authenticateAcpAgent(this.data.agent.id, method.id));
      this.snackBar.open('ACP authentication completed', undefined, { duration: 3000 });
      await this.loadSessions();
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.authenticatingIds.update((current) => without(current, method.id));
    }
  }

  protected async logout(): Promise<void> {
    if (this.logoutLoading()) {
      return;
    }
    this.logoutLoading.set(true);
    this.error.set('');
    try {
      this.inspection.set(await this.api.logoutAcpAgent(this.data.agent.id));
      this.sessions.set([]);
      this.snackBar.open('Signed out from ACP agent', undefined, { duration: 3000 });
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.logoutLoading.set(false);
    }
  }

  protected async loadSessions(): Promise<void> {
    if (!this.inspection()?.sessions.list || this.sessionsLoading()) {
      return;
    }
    this.sessionsLoading.set(true);
    this.error.set('');
    try {
      const sessions = await this.api.listAcpAgentSessions(this.data.agent.id);
      this.sessions.set(sessions ?? []);
      const selections: Record<string, string> = {};
      for (const session of sessions ?? []) {
        const workspace = this.compatibleWorkspaces(session)[0];
        if (workspace) {
          selections[session.id] = workspace.id;
        }
      }
      this.workspaceSelections.set(selections);
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.sessionsLoading.set(false);
    }
  }

  protected async importSession(session: AcpRemoteSession): Promise<void> {
    const workspaceId = this.selectedWorkspace(session);
    if (!workspaceId || this.importingIds().has(session.id)) {
      return;
    }
    this.importingIds.update((current) => new Set(current).add(session.id));
    this.error.set('');
    try {
      await this.api.importAcpAgentSession(this.data.agent.id, {
        remoteSessionId: session.id,
        workspaceId,
        title: session.title ?? '',
      });
      this.importedIds.update((current) => new Set(current).add(session.id));
      await this.appState.refreshSessions();
      this.snackBar.open('ACP session imported', undefined, { duration: 3000 });
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.importingIds.update((current) => without(current, session.id));
    }
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    this.error.set('');
    try {
      const inspection = await this.api.inspectAcpAgent(this.data.agent.id);
      this.inspection.set(inspection);
      await this.loadSessions();
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.loading.set(false);
    }
  }
}

function without(values: Set<string>, value: string): Set<string> {
  const next = new Set(values);
  next.delete(value);
  return next;
}
