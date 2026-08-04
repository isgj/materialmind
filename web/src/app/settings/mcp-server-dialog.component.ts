import { Component, computed, inject, signal } from '@angular/core';
import { FormField, form, maxLength, required } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';

import {
  McpAuthType,
  McpOAuthClientMode,
  McpServer,
  McpServerInput,
  McpTransport,
  McpVariableBinding,
} from '../core/models';

export type McpServerDialogData = McpServer | null;
export type McpServerDialogResult = McpServerInput;

@Component({
  selector: 'app-mcp-server-dialog',
  imports: [
    FormField,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data ? 'Edit MCP server' : 'Add MCP server' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <div class="security-note" role="note">
          <mat-icon aria-hidden="true">key</mat-icon>
          <span>
            Configuration stores environment-variable names only. OAuth refresh tokens are stored in
            the operating-system keyring when available.
          </span>
        </div>

        <div class="field-grid">
          <mat-form-field appearance="outline">
            <mat-label>Name</mat-label>
            <input matInput [formField]="serverForm.name" autocomplete="off" />
            @if (serverForm.name().touched() && serverForm.name().invalid()) {
              <mat-error>Name is required</mat-error>
            }
          </mat-form-field>
          <mat-form-field appearance="outline">
            <mat-label>Transport</mat-label>
            <mat-select [formField]="serverForm.transport">
              <mat-option value="stdio">Local command (stdio)</mat-option>
              <mat-option value="http">Streamable HTTP</mat-option>
            </mat-select>
          </mat-form-field>
        </div>

        @if (model().transport === 'stdio') {
          <mat-form-field appearance="outline">
            <mat-label>Command</mat-label>
            <input
              matInput
              [formField]="serverForm.command"
              autocomplete="off"
              spellcheck="false"
              placeholder="npx"
            />
          </mat-form-field>
          <mat-form-field appearance="outline" subscriptSizing="dynamic">
            <mat-label>Arguments</mat-label>
            <textarea
              matInput
              rows="3"
              [formField]="serverForm.argumentsText"
              placeholder="-y&#10;@modelcontextprotocol/server-filesystem"
            ></textarea>
            <mat-hint>One argument per line</mat-hint>
          </mat-form-field>
          <mat-form-field appearance="outline" subscriptSizing="dynamic">
            <mat-label>Environment mappings</mat-label>
            <textarea
              matInput
              rows="3"
              [formField]="serverForm.environmentText"
              placeholder="GITHUB_TOKEN=MY_GITHUB_TOKEN"
            ></textarea>
            <mat-hint>CHILD_ENV_VAR=BACKEND_ENV_VAR</mat-hint>
          </mat-form-field>
        } @else {
          <mat-form-field appearance="outline">
            <mat-label>Server URL</mat-label>
            <input
              matInput
              type="url"
              [formField]="serverForm.url"
              autocomplete="off"
              spellcheck="false"
              placeholder="https://mcp.example.com/mcp"
            />
          </mat-form-field>
          <mat-form-field appearance="outline" subscriptSizing="dynamic">
            <mat-label>Header mappings</mat-label>
            <textarea
              matInput
              rows="2"
              [formField]="serverForm.headersText"
              placeholder="X-Organization=MY_ORGANIZATION_ID"
            ></textarea>
            <mat-hint>Header-Name=BACKEND_ENV_VAR</mat-hint>
          </mat-form-field>
          <mat-form-field appearance="outline">
            <mat-label>Authentication</mat-label>
            <mat-select [formField]="serverForm.authType">
              <mat-option value="none">None</mat-option>
              <mat-option value="bearer_env">Bearer token from environment</mat-option>
              <mat-option value="oauth">OAuth 2.1</mat-option>
            </mat-select>
          </mat-form-field>

          @if (model().authType === 'bearer_env') {
            <mat-form-field appearance="outline">
              <mat-label>Bearer token env var</mat-label>
              <input
                matInput
                [formField]="serverForm.bearerTokenEnvVar"
                autocomplete="off"
                autocapitalize="none"
                spellcheck="false"
                placeholder="MY_MCP_TOKEN"
              />
            </mat-form-field>
          } @else if (model().authType === 'oauth') {
            <div class="field-grid">
              <mat-form-field appearance="outline">
                <mat-label>OAuth client</mat-label>
                <mat-select [formField]="serverForm.oauthClientMode">
                  <mat-option value="dynamic">Dynamic registration</mat-option>
                  <mat-option value="pre_registered">Pre-registered client</mat-option>
                </mat-select>
              </mat-form-field>
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Scopes (optional)</mat-label>
                <input
                  matInput
                  [formField]="serverForm.oauthScopesText"
                  autocomplete="off"
                  spellcheck="false"
                  placeholder="read write"
                />
                <mat-hint>Space-separated</mat-hint>
              </mat-form-field>
            </div>
            @if (model().oauthClientMode === 'pre_registered') {
              <div class="field-grid">
                <mat-form-field appearance="outline">
                  <mat-label>Client ID</mat-label>
                  <input
                    matInput
                    [formField]="serverForm.oauthClientId"
                    autocomplete="off"
                    spellcheck="false"
                  />
                </mat-form-field>
                <mat-form-field appearance="outline">
                  <mat-label>Client secret env var (optional)</mat-label>
                  <input
                    matInput
                    [formField]="serverForm.oauthClientSecretEnvVar"
                    autocomplete="off"
                    autocapitalize="none"
                    spellcheck="false"
                  />
                </mat-form-field>
              </div>
            }
          }
        }

        @if (mappingError()) {
          <div class="form-error" role="alert">
            <mat-icon aria-hidden="true">error</mat-icon>
            {{ mappingError() }}
          </div>
        }
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="!canSubmit()">
          {{ data ? 'Save' : 'Add server' }}
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      min-width: min(620px, 78vw);
      gap: 12px;
      padding-top: 8px;
    }

    .field-grid {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      gap: 12px;
    }

    .security-note,
    .form-error {
      display: flex;
      align-items: flex-start;
      gap: 10px;
      margin-bottom: 12px;
      border-radius: 6px;
      padding: 12px;
    }

    .security-note {
      background: var(--mat-sys-secondary-container);
      color: var(--mat-sys-on-secondary-container);
      font: var(--mat-sys-body-small);
    }

    .form-error {
      color: var(--mat-sys-error);
    }

    .security-note mat-icon,
    .form-error mat-icon {
      flex: 0 0 auto;
    }

    @media (max-width: 640px) {
      mat-dialog-content {
        min-width: 0;
      }

      .field-grid {
        grid-template-columns: minmax(0, 1fr);
        gap: 12px;
      }
    }
  `,
})
export class McpServerDialogComponent {
  protected readonly data = inject<McpServerDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<McpServerDialogComponent>);
  protected readonly model = signal({
    name: this.data?.name ?? '',
    transport: (this.data?.transport ?? 'stdio') as McpTransport,
    command: this.data?.command ?? '',
    argumentsText: this.data?.arguments.join('\n') ?? '',
    environmentText: formatBindings(this.data?.environment ?? []),
    url: this.data?.url ?? '',
    headersText: formatBindings(this.data?.headers ?? []),
    authType: (this.data?.authType ?? 'none') as McpAuthType,
    bearerTokenEnvVar: this.data?.bearerTokenEnvVar ?? '',
    oauthClientMode: (this.data?.oauthClientMode ?? 'dynamic') as McpOAuthClientMode,
    oauthClientId: this.data?.oauthClientId ?? '',
    oauthClientSecretEnvVar: this.data?.oauthClientSecretEnvVar ?? '',
    oauthScopesText: this.data?.oauthScopes.join(' ') ?? '',
  });
  protected readonly serverForm = form(this.model, (path) => {
    required(path.name);
    maxLength(path.name, 200);
    required(path.transport);
    maxLength(path.command, 4096);
    maxLength(path.argumentsText, 64 * 1024);
    maxLength(path.environmentText, 64 * 1024);
    maxLength(path.url, 8192);
    maxLength(path.headersText, 64 * 1024);
  });
  protected readonly mappingError = computed(() => {
    const value = this.model();
    try {
      parseBindings(value.environmentText);
      parseBindings(value.headersText);
      return '';
    } catch (error) {
      return error instanceof Error ? error.message : 'Invalid environment mapping';
    }
  });
  protected readonly canSubmit = computed(() => {
    const value = this.model();
    if (this.serverForm().invalid() || this.mappingError()) {
      return false;
    }
    if (value.transport === 'stdio') {
      return value.command.trim().length > 0;
    }
    if (!value.url.trim()) {
      return false;
    }
    if (value.authType === 'bearer_env' && !value.bearerTokenEnvVar.trim()) {
      return false;
    }
    return !(
      value.authType === 'oauth' &&
      value.oauthClientMode === 'pre_registered' &&
      !value.oauthClientId.trim()
    );
  });

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (!this.canSubmit()) {
      return;
    }
    const value = this.model();
    this.dialogRef.close({
      name: value.name.trim(),
      transport: value.transport,
      command: value.transport === 'stdio' ? value.command.trim() : '',
      arguments:
        value.transport === 'stdio'
          ? value.argumentsText.split(/\r?\n/).filter((argument) => argument.trim() !== '')
          : [],
      environment: value.transport === 'stdio' ? parseBindings(value.environmentText) : [],
      url: value.transport === 'http' ? value.url.trim() : '',
      headers: value.transport === 'http' ? parseBindings(value.headersText) : [],
      authType: value.transport === 'http' ? value.authType : 'none',
      bearerTokenEnvVar:
        value.transport === 'http' && value.authType === 'bearer_env'
          ? value.bearerTokenEnvVar.trim()
          : '',
      oauthClientMode:
        value.transport === 'http' && value.authType === 'oauth'
          ? value.oauthClientMode
          : 'dynamic',
      oauthClientId:
        value.transport === 'http' &&
        value.authType === 'oauth' &&
        value.oauthClientMode === 'pre_registered'
          ? value.oauthClientId.trim()
          : '',
      oauthClientSecretEnvVar:
        value.transport === 'http' &&
        value.authType === 'oauth' &&
        value.oauthClientMode === 'pre_registered'
          ? value.oauthClientSecretEnvVar.trim()
          : '',
      oauthScopes:
        value.transport === 'http' && value.authType === 'oauth'
          ? value.oauthScopesText.split(/\s+/).filter(Boolean)
          : [],
    } satisfies McpServerDialogResult);
  }
}

function formatBindings(bindings: McpVariableBinding[]): string {
  return bindings.map((binding) => `${binding.name}=${binding.valueEnvVar}`).join('\n');
}

function parseBindings(value: string): McpVariableBinding[] {
  return value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => {
      const separator = line.indexOf('=');
      const name = line.slice(0, separator).trim();
      const valueEnvVar = line.slice(separator + 1).trim();
      if (separator <= 0 || !name || !valueEnvVar) {
        throw new Error(`Mapping on line ${index + 1} must use name=ENV_VAR`);
      }
      return { name, valueEnvVar };
    });
}
