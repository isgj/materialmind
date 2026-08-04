import { Component, computed, inject, signal } from '@angular/core';
import { FormField, form, required } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';

import { AcpAgent, LlmModel, LlmProvider, SessionRuntimeType } from '../core/models';

export interface SessionDialogData {
  title: string;
  runtimeType: SessionRuntimeType;
  selectedLlmModelId: string;
  acpAgentId: string;
  models: LlmModel[];
  providers: LlmProvider[];
  acpAgents: AcpAgent[];
}

export interface SessionDialogResult {
  title: string;
  runtimeType: SessionRuntimeType;
  llmModelId: string | null;
  acpAgentId: string | null;
}

@Component({
  selector: 'app-session-dialog',
  imports: [
    FormField,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.title ? 'Edit session' : 'New session' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <mat-form-field appearance="outline">
          <mat-label>Title</mat-label>
          <input matInput [formField]="sessionForm.title" autocomplete="off" />
          @if (sessionForm.title().touched() && sessionForm.title().invalid()) {
            <mat-error>Title is required</mat-error>
          }
        </mat-form-field>
        @if (editing()) {
          <mat-form-field appearance="outline">
            <mat-label>Agent runtime</mat-label>
            <input matInput [value]="runtimeLabel()" disabled />
          </mat-form-field>
        } @else {
          <mat-form-field appearance="outline">
            <mat-label>Agent runtime</mat-label>
            <mat-select [formField]="sessionForm.runtimeSelection">
              @if (data.models.length > 0) {
                <mat-option value="adk">MaterialMind ADK</mat-option>
              }
              @if (data.acpAgents.length > 0) {
                <mat-optgroup label="ACP agents">
                  @for (agent of data.acpAgents; track agent.id) {
                    <mat-option [value]="'acp:' + agent.id" [disabled]="!agent.available">
                      {{ agent.name }}{{ agent.available ? '' : ' (command unavailable)' }}
                    </mat-option>
                  }
                </mat-optgroup>
              }
            </mat-select>
          </mat-form-field>
        }
        @if (usesAdk()) {
          <mat-form-field appearance="outline">
            <mat-label>Model</mat-label>
            <mat-select [formField]="sessionForm.llmModelId">
              @for (model of data.models; track model.id) {
                <mat-option [value]="model.id">
                  {{ providerName(model) }} / {{ model.name }}
                </mat-option>
              }
            </mat-select>
          </mat-form-field>
        }
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button
          mat-flat-button
          type="submit"
          [disabled]="sessionForm().invalid() || !runtimeSelectionValid()"
        >
          {{ data.title ? 'Save' : 'Create' }}
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      min-width: min(440px, 72vw);
      padding-top: 8px;
    }
  `,
})
export class SessionDialogComponent {
  protected readonly data = inject<SessionDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<SessionDialogComponent>);
  protected readonly editing = computed(() => this.data.title !== '');
  private readonly model = signal({
    title: this.data.title || 'New session',
    runtimeSelection:
      this.data.runtimeType === 'acp' && this.data.acpAgentId
        ? `acp:${this.data.acpAgentId}`
        : this.data.models.length > 0
          ? 'adk'
          : this.data.acpAgents[0]
            ? `acp:${this.data.acpAgents[0].id}`
            : '',
    llmModelId: this.data.selectedLlmModelId || this.data.models[0]?.id || '',
  });
  protected readonly sessionForm = form(this.model, (path) => {
    required(path.title);
    required(path.runtimeSelection);
  });
  protected readonly usesAdk = computed(() => this.model().runtimeSelection === 'adk');
  protected readonly runtimeSelectionValid = computed(() => {
    const selection = this.model().runtimeSelection;
    if (selection === 'adk') {
      return this.model().llmModelId !== '';
    }
    if (this.editing()) {
      return true;
    }
    const agent = this.data.acpAgents.find((item) => `acp:${item.id}` === selection);
    return agent?.available ?? false;
  });
  protected readonly runtimeLabel = computed(() => {
    if (this.data.runtimeType === 'adk') {
      return 'MaterialMind ADK';
    }
    return (
      this.data.acpAgents.find((agent) => agent.id === this.data.acpAgentId)?.name ?? 'ACP agent'
    );
  });

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (this.sessionForm().invalid()) {
      return;
    }
    const value = this.model();
    const runtimeType: SessionRuntimeType = value.runtimeSelection === 'adk' ? 'adk' : 'acp';
    this.dialogRef.close({
      title: value.title.trim(),
      runtimeType,
      llmModelId: runtimeType === 'adk' ? value.llmModelId : null,
      acpAgentId:
        runtimeType === 'acp' ? value.runtimeSelection.replace(/^acp:/, '') || null : null,
    } satisfies SessionDialogResult);
  }

  protected providerName(model: LlmModel): string {
    return this.data.providers.find((provider) => provider.id === model.llmProviderId)?.name ?? '';
  }
}
