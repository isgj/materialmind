import { Component, inject, signal } from '@angular/core';
import { FormField, applyEach, form, required, submit, validate } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';
import { MatTooltipModule } from '@angular/material/tooltip';

import { ToolConfirmationMode, ToolTargetMatcher, ToolTargetRule } from '../core/models';

export interface TargetRulesDialogData {
  toolLabel: string;
  supportedMatchers: ToolTargetMatcher[];
  rules: ToolTargetRule[];
}

@Component({
  selector: 'app-target-rules-dialog',
  imports: [
    FormField,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
    MatTooltipModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.toolLabel }} target rules</h2>
    <form (submit)="save($event)">
      <mat-dialog-content>
        <p>Exact URL rules take priority over origin rules.</p>
        <div class="rules-list">
          @for (ruleField of rulesForm.rules; track $index; let index = $index) {
            <div class="rule-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Match</mat-label>
                <mat-select [formField]="ruleField.matcher">
                  @for (matcher of data.supportedMatchers; track matcher) {
                    <mat-option [value]="matcher">{{ matcherLabel(matcher) }}</mat-option>
                  }
                </mat-select>
              </mat-form-field>
              <mat-form-field class="target-field" appearance="outline" subscriptSizing="dynamic">
                <mat-label>URL</mat-label>
                <input
                  matInput
                  type="url"
                  autocomplete="off"
                  spellcheck="false"
                  [formField]="ruleField.target"
                />
                @if (ruleField.target().touched() && ruleField.target().invalid()) {
                  <mat-error>Enter an absolute HTTP or HTTPS URL</mat-error>
                }
              </mat-form-field>
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Confirmation</mat-label>
                <mat-select [formField]="ruleField.confirmationMode">
                  <mat-option value="allow">Allow without asking</mat-option>
                  <mat-option value="ask">Ask every time</mat-option>
                </mat-select>
              </mat-form-field>
              <button
                mat-icon-button
                type="button"
                matTooltip="Remove rule"
                [attr.aria-label]="'Remove target rule ' + (index + 1)"
                (click)="removeRule(index)"
              >
                <mat-icon>delete</mat-icon>
              </button>
            </div>
          } @empty {
            <div class="empty-rules">
              <mat-icon aria-hidden="true">link_off</mat-icon>
              <span>No target rules</span>
            </div>
          }
        </div>
        <button mat-button type="button" (click)="addRule()">
          <mat-icon>add</mat-icon>
          Add rule
        </button>
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="rulesForm().invalid()">Apply</button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      width: min(900px, 82vw);
      padding-top: 4px;
    }

    p {
      margin: 0 0 16px;
      color: var(--mat-sys-on-surface-variant);
    }

    .rules-list {
      display: grid;
      gap: 10px;
      margin-bottom: 8px;
    }

    .rule-row {
      display: grid;
      grid-template-columns: 150px minmax(240px, 1fr) 190px 48px;
      align-items: start;
      gap: 10px;
    }

    .target-field {
      min-width: 0;
    }

    .empty-rules {
      display: flex;
      min-height: 96px;
      align-items: center;
      justify-content: center;
      gap: 8px;
      color: var(--mat-sys-on-surface-variant);
    }

    @media (max-width: 760px) {
      mat-dialog-content {
        width: 86vw;
      }

      .rule-row {
        grid-template-columns: minmax(0, 1fr) 48px;
      }

      .rule-row mat-form-field {
        grid-column: 1;
      }

      .rule-row button {
        grid-column: 2;
        grid-row: 1;
      }
    }
  `,
})
export class TargetRulesDialogComponent {
  protected readonly data = inject<TargetRulesDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<TargetRulesDialogComponent>);
  private readonly model = signal({
    rules: this.data.rules.map((rule) => ({ ...rule })),
  });
  protected readonly rulesForm = form(this.model, (path) => {
    applyEach(path.rules, (rule) => {
      required(rule.matcher);
      required(rule.target);
      required(rule.confirmationMode);
      validate(rule.target, ({ value }) => {
        try {
          const parsed = new URL(value());
          if (
            (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
            !parsed.username &&
            !parsed.password
          ) {
            return undefined;
          }
        } catch {
          // The validation result below describes malformed URLs.
        }
        return { kind: 'url', message: 'Enter an absolute HTTP or HTTPS URL' };
      });
    });
  });

  protected matcherLabel(matcher: ToolTargetMatcher): string {
    return matcher === 'exact_url' ? 'Exact URL' : 'Origin';
  }

  protected addRule(): void {
    this.model.update((current) => ({
      rules: [
        ...current.rules,
        {
          matcher: this.data.supportedMatchers[0] ?? 'exact_url',
          target: '',
          confirmationMode: 'allow' as ToolConfirmationMode,
        },
      ],
    }));
  }

  protected removeRule(index: number): void {
    this.model.update((current) => ({
      rules: current.rules.filter((_, ruleIndex) => ruleIndex !== index),
    }));
  }

  protected save(event: SubmitEvent): void {
    event.preventDefault();
    void submit(this.rulesForm, async () => {
      this.dialogRef.close(
        this.model().rules.map((rule) => ({ ...rule, target: rule.target.trim() })),
      );
    });
  }
}
