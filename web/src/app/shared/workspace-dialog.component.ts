import { Component, inject, signal } from '@angular/core';
import { FormField, form, required } from '@angular/forms/signals';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatButtonModule } from '@angular/material/button';

export interface WorkspaceDialogData {
  mode: 'create' | 'rename';
  name: string;
  rootPath: string;
}

export interface WorkspaceDialogResult {
  name: string;
  rootPath: string;
}

@Component({
  selector: 'app-workspace-dialog',
  imports: [FormField, MatButtonModule, MatDialogModule, MatFormFieldModule, MatInputModule],
  template: `
    <h2 mat-dialog-title>{{ data.mode === 'create' ? 'Add workspace' : 'Rename workspace' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <mat-form-field appearance="outline">
          <mat-label>Name</mat-label>
          <input matInput [formField]="workspaceForm.name" autocomplete="off" />
          @if (workspaceForm.name().touched() && workspaceForm.name().invalid()) {
            <mat-error>Name is required</mat-error>
          }
        </mat-form-field>
        @if (data.mode === 'create') {
          <mat-form-field appearance="outline">
            <mat-label>Root directory</mat-label>
            <input
              matInput
              [formField]="workspaceForm.rootPath"
              autocomplete="off"
              placeholder="/home/user/projects/example"
            />
            @if (workspaceForm.rootPath().touched() && workspaceForm.rootPath().invalid()) {
              <mat-error>Root directory is required</mat-error>
            }
          </mat-form-field>
        } @else {
          <p class="path">{{ data.rootPath }}</p>
        }
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="workspaceForm().invalid()">
          {{ data.mode === 'create' ? 'Add' : 'Save' }}
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

    .path {
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
      overflow-wrap: anywhere;
    }
  `,
})
export class WorkspaceDialogComponent {
  protected readonly data = inject<WorkspaceDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<WorkspaceDialogComponent>);
  private readonly model = signal({ name: this.data.name, rootPath: this.data.rootPath });
  protected readonly workspaceForm = form(this.model, (path) => {
    required(path.name);
    if (this.data.mode === 'create') {
      required(path.rootPath);
    }
  });

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (this.workspaceForm().invalid()) {
      return;
    }
    const value = this.model();
    this.dialogRef.close({
      name: value.name.trim(),
      rootPath: value.rootPath.trim(),
    } satisfies WorkspaceDialogResult);
  }
}
