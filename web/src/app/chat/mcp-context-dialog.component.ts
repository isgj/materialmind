import { Component, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatTabsModule } from '@angular/material/tabs';

import { ApiService, errorMessage } from '../core/api.service';
import {
  McpPromptExpansion,
  McpPromptSummary,
  McpResourceRead,
  McpResourceTemplateSummary,
  McpSessionContentServer,
} from '../core/models';

export interface McpContextDialogData {
  sessionId: string;
}

export type McpContextDialogResult =
  { kind: 'resource'; value: McpResourceRead } | { kind: 'prompt'; value: McpPromptExpansion };

@Component({
  selector: 'app-mcp-context-dialog',
  imports: [
    MatButtonModule,
    MatDialogModule,
    MatExpansionModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    MatTabsModule,
  ],
  template: `
    <h2 mat-dialog-title>Add MCP context</h2>
    <mat-dialog-content [attr.aria-busy]="loading() || busy()">
      @if (loading()) {
        <div class="dialog-state">
          <mat-spinner diameter="28" aria-label="Loading MCP context"></mat-spinner>
          <span>Loading server content</span>
        </div>
      } @else if (error()) {
        <div class="dialog-state error-state" role="alert">
          <mat-icon>error_outline</mat-icon>
          <span>{{ error() }}</span>
        </div>
      } @else {
        <mat-tab-group>
          <mat-tab [label]="'Resources (' + resourceCount() + ')'">
            <div class="content-list">
              @for (server of servers(); track server.id) {
                @if (server.resources.length || server.resourceTemplates.length) {
                  <section class="server-section">
                    <h3>{{ server.name }}</h3>
                    @for (resource of server.resources; track resource.uri) {
                      <button
                        mat-button
                        type="button"
                        class="content-action"
                        [disabled]="busy()"
                        (click)="selectResource(server, resource.uri)"
                      >
                        <mat-icon>draft</mat-icon>
                        <span>
                          <strong>{{ resource.title || resource.name }}</strong>
                          <small>{{ resource.description || resource.uri }}</small>
                        </span>
                      </button>
                    }
                    @for (template of server.resourceTemplates; track template.uriTemplate) {
                      <mat-expansion-panel>
                        <mat-expansion-panel-header>
                          <mat-panel-title>
                            <mat-icon>variable_add</mat-icon>
                            {{ template.title || template.name }}
                          </mat-panel-title>
                        </mat-expansion-panel-header>
                        <mat-form-field appearance="outline" subscriptSizing="dynamic">
                          <mat-label>Resource URI</mat-label>
                          <input
                            matInput
                            [value]="templateURI(template)"
                            [placeholder]="template.uriTemplate"
                            (input)="setTemplateURI(template, $event)"
                          />
                        </mat-form-field>
                        <button
                          mat-flat-button
                          type="button"
                          [disabled]="busy() || !templateURI(template).trim()"
                          (click)="selectResource(server, templateURI(template))"
                        >
                          Add resource
                        </button>
                      </mat-expansion-panel>
                    }
                  </section>
                }
              }
              @if (resourceCount() === 0) {
                <div class="dialog-state"><span>No resources are advertised.</span></div>
              }
            </div>
          </mat-tab>
          <mat-tab [label]="'Prompts (' + promptCount() + ')'">
            <div class="content-list">
              @for (server of servers(); track server.id) {
                @if (server.prompts.length) {
                  <section class="server-section">
                    <h3>{{ server.name }}</h3>
                    @for (prompt of server.prompts; track prompt.name) {
                      <mat-expansion-panel (opened)="selectPrompt(server, prompt)">
                        <mat-expansion-panel-header>
                          <mat-panel-title>
                            <mat-icon>prompt_suggestion</mat-icon>
                            {{ prompt.title || prompt.name }}
                          </mat-panel-title>
                        </mat-expansion-panel-header>
                        @if (prompt.description) {
                          <p>{{ prompt.description }}</p>
                        }
                        <div class="prompt-fields">
                          @for (argument of prompt.arguments; track argument.name) {
                            <mat-form-field appearance="outline" subscriptSizing="dynamic">
                              <mat-label>{{ argument.title || argument.name }}</mat-label>
                              <input
                                matInput
                                [required]="argument.required"
                                [value]="promptArgument(argument.name)"
                                (input)="setPromptArgument(argument.name, $event)"
                              />
                              @if (argument.description) {
                                <mat-hint>{{ argument.description }}</mat-hint>
                              }
                            </mat-form-field>
                          }
                        </div>
                        <button
                          mat-flat-button
                          type="button"
                          [disabled]="busy() || !promptReady(prompt)"
                          (click)="usePrompt(server, prompt)"
                        >
                          Use prompt
                        </button>
                      </mat-expansion-panel>
                    }
                  </section>
                }
              }
              @if (promptCount() === 0) {
                <div class="dialog-state"><span>No prompts are advertised.</span></div>
              }
            </div>
          </mat-tab>
        </mat-tab-group>
        @if (operationError()) {
          <div class="operation-error" role="alert">{{ operationError() }}</div>
        }
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close>Close</button>
    </mat-dialog-actions>
  `,
  styles: `
    mat-dialog-content {
      min-width: min(680px, 82vw);
      min-height: min(480px, 70vh);
      padding-top: 8px;
    }

    .dialog-state {
      min-height: 280px;
      display: grid;
      place-content: center;
      justify-items: center;
      gap: 12px;
    }

    .error-state,
    .operation-error {
      color: var(--mat-sys-error);
    }

    .content-list {
      display: grid;
      gap: 20px;
      padding: 16px 2px;
    }

    .server-section {
      display: grid;
      gap: 8px;
    }

    h3,
    p {
      margin: 0;
    }

    .content-action {
      min-height: 56px;
      height: auto;
      justify-content: flex-start;
      text-align: left;
    }

    .content-action span,
    .prompt-fields {
      display: grid;
    }

    .content-action small {
      color: var(--mat-sys-on-surface-variant);
      overflow-wrap: anywhere;
    }

    mat-expansion-panel mat-form-field,
    .prompt-fields mat-form-field {
      width: 100%;
    }

    .prompt-fields {
      gap: 12px;
      margin: 12px 0;
    }

    .operation-error {
      padding: 8px 0;
    }
  `,
})
export class McpContextDialogComponent {
  private readonly data = inject<McpContextDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<McpContextDialogComponent>);
  private readonly api = inject(ApiService);

  protected readonly servers = signal<McpSessionContentServer[]>([]);
  protected readonly loading = signal(true);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly operationError = signal('');
  protected readonly promptCount = computed(() =>
    this.servers().reduce((count, server) => count + server.prompts.length, 0),
  );
  protected readonly resourceCount = computed(() =>
    this.servers().reduce(
      (count, server) => count + server.resources.length + server.resourceTemplates.length,
      0,
    ),
  );
  private readonly templateURIs = signal<Record<string, string>>({});
  private readonly promptArguments = signal<Record<string, string>>({});
  private selectedPromptKey = '';

  constructor() {
    void this.load();
  }

  protected templateURI(template: McpResourceTemplateSummary): string {
    return this.templateURIs()[template.uriTemplate] ?? '';
  }

  protected setTemplateURI(template: McpResourceTemplateSummary, event: Event): void {
    const value = (event.target as HTMLInputElement).value;
    this.templateURIs.update((values) => ({ ...values, [template.uriTemplate]: value }));
  }

  protected selectPrompt(server: McpSessionContentServer, prompt: McpPromptSummary): void {
    const key = `${server.id}\u0000${prompt.name}`;
    if (this.selectedPromptKey === key) {
      return;
    }
    this.selectedPromptKey = key;
    this.promptArguments.set({});
    this.operationError.set('');
  }

  protected promptArgument(name: string): string {
    return this.promptArguments()[name] ?? '';
  }

  protected setPromptArgument(name: string, event: Event): void {
    const value = (event.target as HTMLInputElement).value;
    this.promptArguments.update((values) => ({ ...values, [name]: value }));
  }

  protected promptReady(prompt: McpPromptSummary): boolean {
    return prompt.arguments.every(
      (argument) => !argument.required || !!this.promptArgument(argument.name).trim(),
    );
  }

  protected async selectResource(server: McpSessionContentServer, uri: string): Promise<void> {
    if (this.busy()) {
      return;
    }
    this.busy.set(true);
    this.operationError.set('');
    try {
      const value = await this.api.readSessionMcpResource(this.data.sessionId, server.id, uri);
      this.dialogRef.close({ kind: 'resource', value } satisfies McpContextDialogResult);
    } catch (error) {
      this.operationError.set(errorMessage(error));
    } finally {
      this.busy.set(false);
    }
  }

  protected async usePrompt(
    server: McpSessionContentServer,
    prompt: McpPromptSummary,
  ): Promise<void> {
    if (this.busy() || !this.promptReady(prompt)) {
      return;
    }
    this.busy.set(true);
    this.operationError.set('');
    try {
      const args = Object.fromEntries(
        Object.entries(this.promptArguments()).filter(([, value]) => value !== ''),
      );
      const value = await this.api.getSessionMcpPrompt(
        this.data.sessionId,
        server.id,
        prompt.name,
        args,
      );
      this.dialogRef.close({ kind: 'prompt', value } satisfies McpContextDialogResult);
    } catch (error) {
      this.operationError.set(errorMessage(error));
    } finally {
      this.busy.set(false);
    }
  }

  private async load(): Promise<void> {
    try {
      const servers = await this.api.listSessionMcpContent(this.data.sessionId);
      this.servers.set(servers);
      const errors = servers.flatMap((server) =>
        server.error ? [`${server.name}: ${server.error}`] : [],
      );
      if (errors.length > 0 && servers.every((server) => !!server.error)) {
        this.error.set(errors.join('\n'));
      }
    } catch (error) {
      this.error.set(errorMessage(error));
    } finally {
      this.loading.set(false);
    }
  }
}
