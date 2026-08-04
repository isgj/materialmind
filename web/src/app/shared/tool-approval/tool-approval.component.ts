import { Component, computed, input, output, signal } from '@angular/core';
import { FormField, disabled, form, maxLength } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';

import { ToolApprovalDecision, ToolApprovalState } from './tool-approval.models';
import { ToolApprovalOption } from '../../core/models';

@Component({
  selector: 'app-tool-approval',
  imports: [
    FormField,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
  ],
  templateUrl: './tool-approval.component.html',
  styleUrl: './tool-approval.component.scss',
})
export class ToolApprovalComponent {
  readonly approval = input.required<ToolApprovalState>();
  readonly prompt = input.required<string>();
  readonly icon = input('approval');
  readonly targetLabel = input('Target');
  readonly target = input.required<string>();
  readonly showTarget = input(true);
  readonly decision = output<ToolApprovalDecision>();

  private readonly refusalModel = signal({ reason: '' });
  protected readonly refusalForm = form(this.refusalModel, (path) => {
    maxLength(path.reason, 2000);
    disabled(path.reason, { when: () => this.approval().status === 'submitting' });
  });
  protected readonly resolutionText = computed(() => {
    const approval = this.approval();
    const result =
      approval.status === 'executing'
        ? 'Action running'
        : approval.status === 'approved'
          ? 'Action allowed'
          : 'Action refused';

    return approval.reason ? `${result}: ${approval.reason}` : result;
  });

  protected decide(approved: boolean): void {
    this.decision.emit({
      id: this.approval().id,
      approved,
      reason: approved ? '' : this.refusalModel().reason.trim(),
    });
  }

  protected decideOption(option: ToolApprovalOption): void {
    const approved = option.kind.startsWith('allow_');
    this.decision.emit({
      id: this.approval().id,
      approved,
      reason: approved ? '' : this.refusalModel().reason.trim(),
      optionId: option.id,
    });
  }

  protected optionIcon(option: ToolApprovalOption): string {
    if (option.kind === 'allow_always') {
      return 'done_all';
    }
    if (option.kind === 'reject_always') {
      return 'block';
    }
    return option.kind.startsWith('allow_') ? 'check' : 'close';
  }
}
