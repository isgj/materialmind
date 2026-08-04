import { JsonPipe } from '@angular/common';
import {
  Component,
  ElementRef,
  afterRenderEffect,
  input,
  output,
  viewChildren,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatExpansionModule, MatExpansionPanel } from '@angular/material/expansion';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatTooltipModule } from '@angular/material/tooltip';

import { AnsiTextComponent } from '../shared/ansi-text/ansi-text.component';
import { DiffPreviewComponent } from '../shared/diff-preview/diff-preview.component';
import { MCPElicitationComponent } from '../shared/mcp-elicitation/mcp-elicitation.component';
import { MCPElicitationSubmission } from '../shared/mcp-elicitation/mcp-elicitation.models';
import { ToolApprovalComponent } from '../shared/tool-approval/tool-approval.component';
import { ToolApprovalDecision } from '../shared/tool-approval/tool-approval.models';
import { UserInputComponent } from '../shared/user-input/user-input.component';
import { UserInputSubmission } from '../shared/user-input/user-input.models';
import { AgentActivityStep, CommandActivityDetails, FetchActivityDetails } from './chat-timeline';
import { McpToolDetails } from './mcp-tool-details/mcp-tool-details';
import { SessionNotesDetailsComponent } from './session-notes-details/session-notes-details';

const compactCompletedTools = new Set([
  'context_compaction',
  'grep',
  'list_directory',
  'load_skill',
  'read_directory',
  'read_file',
  'read_session_notes',
]);

@Component({
  selector: 'app-agent-activity',
  imports: [
    JsonPipe,
    AnsiTextComponent,
    DiffPreviewComponent,
    MCPElicitationComponent,
    McpToolDetails,
    SessionNotesDetailsComponent,
    MatButtonModule,
    MatExpansionModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatTooltipModule,
    ToolApprovalComponent,
    UserInputComponent,
  ],
  templateUrl: './agent-activity.component.html',
  styleUrl: './agent-activity.component.scss',
})
export class AgentActivityComponent {
  readonly sessionId = input('');
  readonly steps = input.required<readonly AgentActivityStep[]>();
  readonly running = input(false);
  readonly approvalDecision = output<ToolApprovalDecision>();
  readonly userInputSubmission = output<UserInputSubmission>();
  readonly mcpElicitationSubmission = output<MCPElicitationSubmission>();
  readonly mcpCancellation = output<string>();
  readonly activeStepExpanded = output<void>();

  private readonly activityPanels = viewChildren<MatExpansionPanel>('activityPanel');
  private readonly commandOutputElements =
    viewChildren<ElementRef<HTMLPreElement>>('commandOutput');
  private activityStatuses = new Map<string, AgentActivityStep['status']>();

  constructor() {
    afterRenderEffect(() => {
      const steps = this.steps();
      const panels = this.activityPanels();
      if (panels.length !== steps.length) {
        return;
      }

      const nextStatuses = new Map<string, AgentActivityStep['status']>();
      for (const [index, step] of steps.entries()) {
        const previousStatus = this.activityStatuses.get(step.id);
        const panel = panels[index];

        if (previousStatus === undefined && this.shouldOpenInitially(step)) {
          panel.open();
        } else if (
          previousStatus !== undefined &&
          this.isWaitingForUser(step.status) &&
          !this.isWaitingForUser(previousStatus)
        ) {
          panel.open();
        } else if (
          previousStatus !== undefined &&
          this.isTerminal(step.status) &&
          !this.isTerminal(previousStatus)
        ) {
          panel.close();
        }
        nextStatuses.set(step.id, step.status);
      }
      this.activityStatuses = nextStatuses;
    });
    afterRenderEffect(() => {
      const runningCommandIds = new Set(
        this.steps()
          .filter((step) => step.status === 'running' && step.command)
          .map((step) => step.id),
      );
      for (const output of this.commandOutputElements()) {
        if (runningCommandIds.has(output.nativeElement.dataset['commandStepId'] ?? '')) {
          output.nativeElement.scrollTop = output.nativeElement.scrollHeight;
        }
      }
    });
  }

  protected hasValues(value: Record<string, unknown> | undefined): boolean {
    return !!value && Object.keys(value).length > 0;
  }

  protected mcpArguments(step: AgentActivityStep): Record<string, unknown> {
    const argumentsValue = step.input?.['arguments'];
    return isRecord(argumentsValue) ? argumentsValue : (step.input ?? {});
  }

  protected showStepDetails(step: AgentActivityStep): boolean {
    return (
      !!step.userInput ||
      step.status !== 'complete' ||
      !compactCompletedTools.has(step.toolName ?? '')
    );
  }

  protected statusLabel(step: AgentActivityStep): string {
    if (
      step.approval?.status === 'submitting' ||
      step.userInput?.status === 'submitting' ||
      step.mcpElicitation?.status === 'submitting'
    ) {
      return 'Submitting';
    }
    switch (step.status) {
      case 'approval_required':
        return 'Approval needed';
      case 'input_required':
        return 'Response needed';
      case 'queued':
        return 'Approved, waiting to run';
      case 'running':
        return 'Running';
      case 'incomplete':
        return 'No result';
      case 'denied':
        return 'Denied';
      case 'cancelled':
        return 'Cancelled';
      case 'failed':
        return 'Failed';
      default:
        return 'Complete';
    }
  }

  protected statusIcon(step: AgentActivityStep): string {
    switch (step.status) {
      case 'approval_required':
        return 'pending_actions';
      case 'input_required':
        return 'contact_support';
      case 'queued':
        return 'schedule';
      case 'denied':
        return 'block';
      case 'cancelled':
        return 'cancel';
      case 'failed':
        return 'error_outline';
      case 'incomplete':
        return 'help_outline';
      default:
        return 'check';
    }
  }

  protected commandStatus(
    command: CommandActivityDetails,
    status: AgentActivityStep['status'],
  ): string {
    if (status === 'queued') {
      return 'Approved, waiting to run';
    }
    if (status === 'running') {
      return 'Running';
    }
    if (command.state === 'denied') {
      return 'Command refused';
    }
    if (command.timedOut) {
      return `Timed out after ${command.timeoutSeconds} seconds`;
    }
    if (command.exitCode !== undefined) {
      const duration =
        command.durationMs === undefined ? '' : ` in ${formatDuration(command.durationMs)}`;
      return `Exited with code ${command.exitCode}${duration}`;
    }
    if (status === 'failed' || command.state === 'failed' || command.error) {
      return 'Run exited with error';
    }
    return 'Command finished';
  }

  protected hasCommandOutput(command: CommandActivityDetails): boolean {
    return command.stdout !== '' || command.stderr !== '';
  }

  protected commandOutputLabel(command: CommandActivityDetails): string {
    if (command.stdout && command.stderr) {
      return 'stdout and stderr';
    }
    return command.stderr ? 'stderr' : 'stdout';
  }

  protected fetchStatusLabel(fetch: FetchActivityDetails): string {
    if (fetch.httpStatus !== undefined) {
      return `HTTP ${fetch.httpStatus}`;
    }
    if (fetch.state === 'denied') {
      return 'Request refused';
    }
    return fetch.state === 'fetched' ? 'Response received' : 'Request finished';
  }

  protected fetchStatusIcon(fetch: FetchActivityDetails): string {
    if (fetch.state === 'denied') {
      return 'block';
    }
    return fetch.httpStatus !== undefined && fetch.httpStatus >= 400
      ? 'error_outline'
      : 'cloud_done';
  }

  protected fetchFailed(fetch: FetchActivityDetails): boolean {
    return fetch.state === 'denied' || (fetch.httpStatus !== undefined && fetch.httpStatus >= 400);
  }

  protected cancelMCPToolCall(event: MouseEvent, toolCallId: string): void {
    event.stopPropagation();
    this.mcpCancellation.emit(toolCallId);
  }

  protected notifyActiveStepExpanded(step: AgentActivityStep): void {
    if (this.isActive(step.status)) {
      this.activeStepExpanded.emit();
    }
  }

  private isActive(status: AgentActivityStep['status']): boolean {
    return status === 'running' || status === 'approval_required' || status === 'input_required';
  }

  private shouldOpenInitially(step: AgentActivityStep): boolean {
    return (
      this.isWaitingForUser(step.status) || (step.status === 'running' && this.isCommandStep(step))
    );
  }

  private isCommandStep(step: AgentActivityStep): boolean {
    return (
      step.toolName === 'run_command' ||
      step.command !== undefined ||
      step.approval?.kind === 'command_run'
    );
  }

  private isWaitingForUser(status: AgentActivityStep['status']): boolean {
    return status === 'approval_required' || status === 'input_required';
  }

  private isTerminal(status: AgentActivityStep['status']): boolean {
    return (
      status === 'complete' ||
      status === 'incomplete' ||
      status === 'denied' ||
      status === 'cancelled' ||
      status === 'failed'
    );
  }
}

function formatDuration(milliseconds: number): string {
  return milliseconds < 1000
    ? `${milliseconds} ms`
    : `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 1 : 0)} s`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
