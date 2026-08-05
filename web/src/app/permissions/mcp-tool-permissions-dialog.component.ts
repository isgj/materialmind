import { Component, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatSelectModule } from '@angular/material/select';
import { MatTableModule } from '@angular/material/table';

import { McpToolPermission, McpToolSummary, ToolConfirmationMode } from '../core/models';

type McpToolMode = ToolConfirmationMode | 'inherit';

export interface McpToolPermissionsDialogData {
  serverName: string;
  defaultConfirmation: ToolConfirmationMode;
  tools: McpToolSummary[];
  permissions: McpToolPermission[];
}

@Component({
  selector: 'app-mcp-tool-permissions-dialog',
  imports: [
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    MatTableModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.serverName }} tools</h2>
    <mat-dialog-content>
      <p>Tools inherit the server policy unless an explicit confirmation rule is selected.</p>
      <div class="table-region">
        <table mat-table [dataSource]="data.tools" aria-label="MCP tool permissions">
          <ng-container matColumnDef="tool">
            <th mat-header-cell *matHeaderCellDef>Tool</th>
            <td mat-cell *matCellDef="let tool">
              <div class="tool">
                <mat-icon aria-hidden="true">build</mat-icon>
                <div>
                  <strong>{{ tool.name }}</strong>
                  @if (tool.description) {
                    <span>{{ tool.description }}</span>
                  }
                </div>
              </div>
            </td>
          </ng-container>

          <ng-container matColumnDef="confirmation">
            <th mat-header-cell *matHeaderCellDef>Confirmation</th>
            <td mat-cell *matCellDef="let tool">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Confirmation</mat-label>
                <mat-select
                  [value]="mode(tool.name)"
                  (selectionChange)="setMode(tool.name, $event.value)"
                >
                  <mat-option value="inherit">
                    Inherit: {{ confirmationLabel(data.defaultConfirmation) }}
                  </mat-option>
                  <mat-option value="allow">Allow without asking</mat-option>
                  <mat-option value="ask">Ask every time</mat-option>
                </mat-select>
              </mat-form-field>
            </td>
          </ng-container>

          <tr mat-header-row *matHeaderRowDef="displayedColumns"></tr>
          <tr mat-row *matRowDef="let row; columns: displayedColumns"></tr>
        </table>
      </div>
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" (click)="cancel()">Cancel</button>
      <button mat-flat-button type="button" (click)="save()">Apply</button>
    </mat-dialog-actions>
  `,
  styles: `
    mat-dialog-content {
      min-width: min(720px, 80vw);
      padding-top: 8px;
    }

    p {
      margin: 0 0 12px;
      color: var(--mat-sys-on-surface-variant);
    }

    .table-region {
      max-height: min(560px, 65vh);
      overflow: auto;
    }

    table {
      width: 100%;
      background: transparent;
    }

    .mat-column-confirmation {
      width: 260px;
    }

    td.mat-mdc-cell {
      padding-block: 10px;
      vertical-align: top;
    }

    .tool {
      display: grid;
      grid-template-columns: 24px minmax(0, 1fr);
      align-items: flex-start;
      gap: 10px;
      padding-top: 8px;
    }

    .tool > div {
      display: grid;
      min-width: 0;
      gap: 2px;
    }

    .tool mat-icon {
      width: 24px;
      height: 24px;
      font-size: 24px;
    }

    .tool strong {
      font: var(--mat-sys-title-small);
    }

    .tool span {
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
      overflow-wrap: anywhere;
    }

    mat-form-field {
      width: 100%;
    }

    @media (max-width: 680px) {
      mat-dialog-content {
        min-width: 0;
      }

      .mat-column-confirmation {
        width: 220px;
      }
    }
  `,
})
export class McpToolPermissionsDialogComponent {
  protected readonly data = inject<McpToolPermissionsDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<McpToolPermissionsDialogComponent>);
  private readonly modes = signal<Record<string, McpToolMode>>(
    Object.fromEntries(
      this.data.permissions.map((permission) => [permission.toolName, permission.confirmationMode]),
    ),
  );
  protected readonly displayedColumns = ['tool', 'confirmation'];
  private readonly result = computed<McpToolPermission[]>(() =>
    Object.entries(this.modes())
      .filter((entry): entry is [string, ToolConfirmationMode] => entry[1] !== 'inherit')
      .map(([toolName, confirmationMode]) => ({ toolName, confirmationMode }))
      .sort((left, right) => left.toolName.localeCompare(right.toolName)),
  );

  protected mode(toolName: string): McpToolMode {
    return this.modes()[toolName] ?? 'inherit';
  }

  protected setMode(toolName: string, mode: McpToolMode): void {
    this.modes.update((current) => ({ ...current, [toolName]: mode }));
  }

  protected confirmationLabel(mode: ToolConfirmationMode): string {
    return mode === 'allow' ? 'allow without asking' : 'ask every time';
  }

  protected cancel(): void {
    this.dialogRef.close();
  }

  protected save(): void {
    this.dialogRef.close(this.result());
  }
}
