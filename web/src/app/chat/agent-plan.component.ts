import { Component, computed, input } from '@angular/core';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatTooltipModule } from '@angular/material/tooltip';

import { AgentPlan, AgentPlanEntry, AgentPlanEntryStatus } from '../core/models';

@Component({
  selector: 'app-agent-plan',
  imports: [MatExpansionModule, MatIconModule, MatProgressBarModule, MatTooltipModule],
  templateUrl: './agent-plan.component.html',
  styleUrl: './agent-plan.component.scss',
})
export class AgentPlanComponent {
  readonly plan = input.required<AgentPlan>();

  protected readonly completedCount = computed(
    () => this.plan().entries.filter((entry) => entry.status === 'completed').length,
  );
  protected readonly progress = computed(() => {
    const total = this.plan().entries.length;
    return total === 0 ? 0 : (this.completedCount() / total) * 100;
  });
  protected readonly progressLabel = computed(
    () => `${this.completedCount()} of ${this.plan().entries.length} tasks completed`,
  );

  protected statusIcon(entry: AgentPlanEntry): string {
    switch (entry.status) {
      case 'completed':
        return 'check_circle';
      case 'in_progress':
        return 'progress_activity';
      default:
        return 'radio_button_unchecked';
    }
  }

  protected statusLabel(status: AgentPlanEntryStatus): string {
    switch (status) {
      case 'completed':
        return 'Complete';
      case 'in_progress':
        return 'In progress';
      default:
        return 'Pending';
    }
  }
}
