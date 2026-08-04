import { Component, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';

import { MarkdownPipe } from '../../shared/markdown.pipe';
import { SessionNotesActivityDetails } from '../chat-timeline';

@Component({
  selector: 'app-session-notes-details',
  imports: [MarkdownPipe, MatIconModule, MatTooltipModule],
  templateUrl: './session-notes-details.html',
  styleUrl: './session-notes-details.scss',
})
export class SessionNotesDetailsComponent {
  readonly details = input.required<SessionNotesActivityDetails>();
}
