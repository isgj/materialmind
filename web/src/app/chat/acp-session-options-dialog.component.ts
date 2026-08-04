import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatSelectModule } from '@angular/material/select';
import { MatSlideToggleModule } from '@angular/material/slide-toggle';

import {
  AcpConfigOption,
  AcpConfigSelectGroup,
  AcpConfigSelectOption,
  AcpConfigSelectValue,
} from '../core/models';

export interface AcpSessionOptionsDialogData {
  options: AcpConfigOption[];
}

export interface AcpSessionOptionChange {
  id: string;
  value: string | boolean;
}

@Component({
  selector: 'app-acp-session-options-dialog',
  imports: [
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatSelectModule,
    MatSlideToggleModule,
  ],
  template: `
    <h2 mat-dialog-title>ACP session options</h2>
    <mat-dialog-content>
      @for (option of data.options; track option.id) {
        @if (option.type === 'select') {
          <mat-form-field appearance="outline">
            <mat-label>{{ option.name }}</mat-label>
            <mat-select
              [value]="value(option)"
              (selectionChange)="setValue(option.id, $event.value)"
            >
              @for (entry of option.options; track optionTrackKey(entry)) {
                @if (isGroup(entry)) {
                  <mat-optgroup [label]="entry.name">
                    @for (item of entry.options; track item.value) {
                      <mat-option [value]="item.value">{{ item.name }}</mat-option>
                    }
                  </mat-optgroup>
                } @else {
                  <mat-option [value]="entry.value">{{ entry.name }}</mat-option>
                }
              }
            </mat-select>
            @if (option.description) {
              <mat-hint>{{ option.description }}</mat-hint>
            }
          </mat-form-field>
        } @else {
          <div class="boolean-option">
            <mat-slide-toggle
              [checked]="booleanValue(option)"
              (change)="setValue(option.id, $event.checked)"
            >
              {{ option.name }}
            </mat-slide-toggle>
            @if (option.description) {
              <span>{{ option.description }}</span>
            }
          </div>
        }
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close>Cancel</button>
      <button mat-flat-button type="button" (click)="submit()">Apply</button>
    </mat-dialog-actions>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      min-width: min(480px, 76vw);
      gap: 16px;
    }

    .mat-mdc-dialog-title + mat-dialog-content {
      padding-top: 20px;
    }

    .boolean-option {
      display: grid;
      gap: 4px;
      padding: 8px 0 16px;
    }

    .boolean-option > span {
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
    }
  `,
})
export class AcpSessionOptionsDialogComponent {
  protected readonly data = inject<AcpSessionOptionsDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<AcpSessionOptionsDialogComponent>);
  private readonly values = signal<Record<string, string | boolean>>(
    Object.fromEntries(
      this.data.options.map((option) => [option.id, option.currentValue] as const),
    ),
  );

  protected value(option: AcpConfigSelectOption): string {
    return String(this.values()[option.id] ?? option.currentValue);
  }

  protected booleanValue(option: AcpConfigOption): boolean {
    return this.values()[option.id] === true;
  }

  protected setValue(id: string, value: string | boolean): void {
    this.values.update((current) => ({ ...current, [id]: value }));
  }

  protected isGroup(
    option: AcpConfigSelectValue | AcpConfigSelectGroup,
  ): option is AcpConfigSelectGroup {
    return 'options' in option;
  }

  protected optionTrackKey(option: AcpConfigSelectValue | AcpConfigSelectGroup): string {
    return this.isGroup(option) ? `group:${option.group}` : `value:${option.value}`;
  }

  protected submit(): void {
    const values = this.values();
    const changes = this.data.options
      .filter((option) => values[option.id] !== option.currentValue)
      .map((option) => ({ id: option.id, value: values[option.id] ?? option.currentValue }));
    this.dialogRef.close(changes satisfies AcpSessionOptionChange[]);
  }
}
