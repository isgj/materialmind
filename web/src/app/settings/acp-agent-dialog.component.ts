import { Component, inject, signal } from '@angular/core';
import { FormField, form, maxLength, required } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';

import { AcpAgent } from '../core/models';

export type AcpAgentDialogData = AcpAgent | null;

export interface AcpAgentDialogResult {
  name: string;
  command: string;
  arguments: string[];
}

@Component({
  selector: 'app-acp-agent-dialog',
  imports: [
    FormField,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data ? 'Edit ACP agent' : 'Add ACP agent' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <div class="trust-notice" role="note">
          <mat-icon aria-hidden="true">security</mat-icon>
          <span>
            This command runs as a trusted local process with the backend user's filesystem and
            environment access.
          </span>
        </div>
        <mat-form-field appearance="outline">
          <mat-label>Name</mat-label>
          <input matInput [formField]="agentForm.name" autocomplete="off" />
          @if (agentForm.name().touched() && agentForm.name().invalid()) {
            <mat-error>Name is required</mat-error>
          }
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Command</mat-label>
          <input
            matInput
            [formField]="agentForm.command"
            autocomplete="off"
            placeholder="codex-acp"
          />
          @if (agentForm.command().touched() && agentForm.command().invalid()) {
            <mat-error>Command is required</mat-error>
          }
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Arguments</mat-label>
          <textarea
            matInput
            rows="4"
            [formField]="agentForm.argumentsText"
            placeholder="--flag&#10;value"
          ></textarea>
          <mat-hint>One argument per line</mat-hint>
        </mat-form-field>
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="agentForm().invalid()">
          {{ data ? 'Save' : 'Add agent' }}
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      min-width: min(480px, 76vw);
      gap: 4px;
      padding-top: 8px;
    }

    .trust-notice {
      display: flex;
      align-items: flex-start;
      gap: 10px;
      margin-bottom: 12px;
      border-radius: 6px;
      background: var(--mat-sys-secondary-container);
      color: var(--mat-sys-on-secondary-container);
      font: var(--mat-sys-body-small);
      padding: 12px;
    }

    .trust-notice mat-icon {
      flex: 0 0 auto;
    }
  `,
})
export class AcpAgentDialogComponent {
  protected readonly data = inject<AcpAgentDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<AcpAgentDialogComponent>);
  private readonly model = signal({
    name: this.data?.name ?? '',
    command: this.data?.command ?? '',
    argumentsText: this.data?.arguments.join('\n') ?? '',
  });
  protected readonly agentForm = form(this.model, (path) => {
    required(path.name);
    maxLength(path.name, 200);
    required(path.command);
    maxLength(path.command, 4096);
    maxLength(path.argumentsText, 64 * 1024);
  });

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (this.agentForm().invalid()) {
      return;
    }
    const value = this.model();
    this.dialogRef.close({
      name: value.name.trim(),
      command: value.command.trim(),
      arguments: value.argumentsText.split(/\r?\n/).filter((argument) => argument.trim() !== ''),
    } satisfies AcpAgentDialogResult);
  }
}
