import {
  Component,
  ElementRef,
  afterRenderEffect,
  computed,
  effect,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatTooltipModule } from '@angular/material/tooltip';

import { MarkdownPipe } from '../shared/markdown.pipe';
import { ToolApprovalDecision } from '../shared/tool-approval/tool-approval.models';
import { MCPElicitationSubmission } from '../shared/mcp-elicitation/mcp-elicitation.models';
import { UserInputSubmission } from '../shared/user-input/user-input.models';
import { AgentActivityComponent } from './agent-activity.component';
import { AgentNoteComponent } from './agent-note.component';
import {
  ActivityStatus,
  AgentDelegation as AgentDelegationView,
  effectiveDelegationStatus,
} from './chat-timeline';

@Component({
  selector: 'app-agent-delegation',
  imports: [
    AgentActivityComponent,
    AgentNoteComponent,
    MarkdownPipe,
    MatExpansionModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatTooltipModule,
  ],
  templateUrl: './agent-delegation.html',
  styleUrl: './agent-delegation.scss',
})
export class AgentDelegationComponent {
  readonly sessionId = input('');
  readonly delegation = input.required<AgentDelegationView>();
  readonly approvalDecision = output<ToolApprovalDecision>();
  readonly userInputSubmission = output<UserInputSubmission>();
  readonly mcpElicitationSubmission = output<MCPElicitationSubmission>();
  readonly mcpCancellation = output<string>();
  protected readonly expanded = signal(false);
  protected readonly effectiveStatus = computed(() => effectiveDelegationStatus(this.delegation()));

  private readonly delegationBody = viewChild<ElementRef<HTMLElement>>('delegationBody');
  private previousStatus: ActivityStatus | null = null;
  private previousActivityState: string | null = null;

  constructor() {
    effect(() => {
      const status = this.effectiveStatus();
      if (this.isActive(status)) {
        this.expanded.set(true);
      } else if (this.previousStatus && this.isActive(this.previousStatus)) {
        this.expanded.set(false);
      }
      this.previousStatus = status;
    });
    afterRenderEffect(() => {
      const activityState = this.activityState();
      const activityChanged =
        this.previousActivityState !== null && activityState !== this.previousActivityState;
      const initialActive =
        this.previousActivityState === null && this.isActive(this.effectiveStatus());
      this.previousActivityState = activityState;
      if ((!activityChanged && !initialActive) || !this.expanded()) {
        return;
      }
      this.scrollToLatestActivity();
    });
  }

  protected scrollToLatestActivity(): void {
    const body = this.delegationBody()?.nativeElement;
    if (body) {
      body.scrollTop = body.scrollHeight;
    }
  }

  private activityState(): string {
    return this.delegation()
      .timeline.map((entry) => {
        if (entry.kind !== 'activity') {
          return `${entry.kind}:${entry.id}`;
        }
        const steps = entry.steps.map((step) => {
          const approval = step.approval?.status ?? '';
          const userInput = step.userInput?.status ?? '';
          const elicitation = step.mcpElicitation?.status ?? '';
          return `${step.id}:${step.status}:${approval}:${userInput}:${elicitation}`;
        });
        return `${entry.kind}:${entry.id}:${steps.join(',')}`;
      })
      .join('|');
  }

  protected isActive(status: ActivityStatus): boolean {
    return (
      status === 'running' ||
      status === 'queued' ||
      status === 'approval_required' ||
      status === 'input_required'
    );
  }

  protected statusLabel(status: ActivityStatus): string {
    switch (status) {
      case 'running':
        return 'Running';
      case 'queued':
        return 'Approved, waiting to run';
      case 'approval_required':
        return 'Waiting for approval';
      case 'input_required':
        return 'Waiting for input';
      case 'complete':
        return 'Complete';
      case 'denied':
        return 'Denied';
      case 'cancelled':
        return 'Cancelled';
      case 'failed':
        return 'Failed';
      case 'incomplete':
        return 'Incomplete';
    }
  }

  protected statusIcon(status: ActivityStatus): string {
    switch (status) {
      case 'approval_required':
        return 'approval';
      case 'input_required':
        return 'question_answer';
      case 'complete':
        return 'check_circle';
      case 'denied':
        return 'block';
      case 'cancelled':
        return 'cancel';
      case 'failed':
        return 'error';
      case 'incomplete':
        return 'pending';
      case 'running':
        return 'progress_activity';
      case 'queued':
        return 'schedule';
    }
  }
}
