import { Component, computed, inject, signal } from '@angular/core';
import { FormField, form, required } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';

import { LlmProvider, LlmProviderInput } from '../core/models';

export type ProviderDialogData = Pick<
  LlmProvider,
  | 'id'
  | 'name'
  | 'apiCompatibility'
  | 'baseUrl'
  | 'authType'
  | 'bearerTokenEnvVar'
  | 'credentialAvailable'
> | null;

export type ProviderDialogResult = LlmProviderInput;

@Component({
  selector: 'app-provider-dialog',
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
    <h2 mat-dialog-title>{{ data ? 'Edit LLM provider' : 'Add LLM provider' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <mat-form-field appearance="outline">
          <mat-label>Name</mat-label>
          <input matInput [formField]="providerForm.name" autocomplete="off" />
          @if (providerForm.name().touched() && providerForm.name().invalid()) {
            <mat-error>Name is required</mat-error>
          }
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>API compatibility</mat-label>
          <mat-select [formField]="providerForm.apiCompatibility">
            <mat-option value="anthropic">Anthropic compatible</mat-option>
            <mat-option value="gemini">Google Gemini API</mat-option>
            <mat-option value="openai-chat-completions">
              OpenAI compatible (Chat Completions)
            </mat-option>
            <mat-option value="openai-responses">OpenAI compatible (Responses)</mat-option>
          </mat-select>
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Base URL (optional)</mat-label>
          <input
            matInput
            type="url"
            [formField]="providerForm.baseUrl"
            autocomplete="off"
            spellcheck="false"
            placeholder="https://gateway.example.com/v1"
          />
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Authentication</mat-label>
          <mat-select [formField]="providerForm.authType">
            <mat-option value="bearer_keyring">OS keyring</mat-option>
            <mat-option value="bearer_env">Environment variable</mat-option>
            <mat-option value="none">None</mat-option>
          </mat-select>
        </mat-form-field>
        @if (model().authType === 'bearer_keyring') {
          <mat-form-field appearance="outline">
            <mat-label>{{ keyringCredentialLabel() }}</mat-label>
            <input
              matInput
              [type]="tokenVisible() ? 'text' : 'password'"
              [formField]="providerForm.bearerToken"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
            />
            <button
              mat-icon-button
              matSuffix
              type="button"
              [attr.aria-label]="(tokenVisible() ? 'Hide ' : 'Show ') + credentialSentenceLabel()"
              (click)="tokenVisible.update((visible) => !visible)"
            >
              <mat-icon>{{ tokenVisible() ? 'visibility_off' : 'visibility' }}</mat-icon>
            </button>
            @if (providerForm.bearerToken().touched() && providerForm.bearerToken().invalid()) {
              <mat-error>{{ credentialLabel() }} is required</mat-error>
            } @else if (hasStoredKeyringCredential) {
              <mat-hint>Leave blank to keep the stored credential</mat-hint>
            } @else {
              <mat-hint>The credential is stored outside the MaterialMind database</mat-hint>
            }
          </mat-form-field>
        } @else if (model().authType === 'bearer_env') {
          <mat-form-field appearance="outline">
            <mat-label>{{ credentialLabel() }} environment variable</mat-label>
            <input
              matInput
              [formField]="providerForm.bearerTokenEnvVar"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              [placeholder]="credentialEnvironmentPlaceholder()"
            />
            @if (
              providerForm.bearerTokenEnvVar().touched() &&
              providerForm.bearerTokenEnvVar().invalid()
            ) {
              <mat-error>Environment variable name is required</mat-error>
            }
          </mat-form-field>
        }
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="providerForm().invalid()">
          {{ data ? 'Save' : 'Add' }}
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      row-gap: 8px;
      min-width: min(520px, 72vw);
      padding-top: 8px;
    }
  `,
})
export class ProviderDialogComponent {
  protected readonly data = inject<ProviderDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<ProviderDialogComponent>);
  protected readonly model = signal({
    name: this.data?.name ?? '',
    apiCompatibility: this.data?.apiCompatibility ?? 'anthropic',
    baseUrl: this.data?.baseUrl ?? '',
    authType: this.data?.authType ?? 'bearer_keyring',
    bearerTokenEnvVar: this.data?.bearerTokenEnvVar ?? '',
    bearerToken: '',
  });
  protected readonly hasStoredKeyringCredential =
    this.data?.authType === 'bearer_keyring' && this.data.credentialAvailable;
  protected readonly tokenVisible = signal(false);
  protected readonly credentialLabel = computed(() =>
    this.model().apiCompatibility === 'gemini' ? 'API key' : 'Bearer token',
  );
  protected readonly credentialSentenceLabel = computed(() =>
    this.model().apiCompatibility === 'gemini' ? 'API key' : 'bearer token',
  );
  protected readonly keyringCredentialLabel = computed(() =>
    this.hasStoredKeyringCredential
      ? `Replace ${this.credentialSentenceLabel()} (optional)`
      : this.credentialLabel(),
  );
  protected readonly credentialEnvironmentPlaceholder = computed(() =>
    this.model().apiCompatibility === 'gemini' ? 'MY_GEMINI_API_KEY' : 'MY_LLM_TOKEN',
  );
  protected readonly providerForm = form(this.model, (path) => {
    required(path.name);
    required(path.apiCompatibility);
    required(path.authType);
    required(path.bearerTokenEnvVar, {
      when: ({ valueOf }) => valueOf(path.authType) === 'bearer_env',
    });
    required(path.bearerToken, {
      when: ({ valueOf }) =>
        valueOf(path.authType) === 'bearer_keyring' && !this.hasStoredKeyringCredential,
    });
  });

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (this.providerForm().invalid()) {
      return;
    }
    const value = this.model();
    this.dialogRef.close({
      name: value.name.trim(),
      apiCompatibility: value.apiCompatibility,
      baseUrl: value.baseUrl.trim(),
      authType: value.authType,
      bearerTokenEnvVar: value.authType === 'bearer_env' ? value.bearerTokenEnvVar.trim() : '',
      bearerToken: value.authType === 'bearer_keyring' ? value.bearerToken.trim() : '',
    } satisfies ProviderDialogResult);
  }
}
