import { Component, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';

import { MarkdownPipe } from '../shared/markdown.pipe';

@Component({
  selector: 'app-agent-note',
  imports: [MarkdownPipe, MatIconModule, MatTooltipModule],
  template: `
    <article class="agent-note" aria-label="Agent note">
      <mat-icon aria-hidden="true" matTooltip="Agent note">psychology</mat-icon>
      <div class="markdown-body" [innerHTML]="text() | markdown"></div>
    </article>
  `,
  styles: `
    :host {
      display: block;
      width: 100%;
    }

    .agent-note {
      display: grid;
      grid-template-columns: 24px minmax(0, 1fr);
      align-items: start;
      gap: 10px;
      color: var(--mat-sys-on-surface);
    }

    mat-icon {
      width: 22px;
      height: 22px;
      margin-top: 1px;
      color: var(--mat-sys-primary);
      font-size: 22px;
    }

    .markdown-body {
      min-width: 0;
      font: var(--mat-sys-body-medium);
      line-height: 1.58;
      overflow-wrap: anywhere;
    }
  `,
})
export class AgentNoteComponent {
  readonly text = input.required<string>();
}
