import { DatePipe } from '@angular/common';
import { Component, inject } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule } from '@angular/material/dialog';
import { MatIconModule } from '@angular/material/icon';

import { SessionNotes } from '../../core/models';
import { MarkdownPipe } from '../../shared/markdown.pipe';

@Component({
  selector: 'app-session-notes-dialog',
  imports: [DatePipe, MarkdownPipe, MatButtonModule, MatDialogModule, MatIconModule],
  templateUrl: './session-notes-dialog.html',
  styleUrl: './session-notes-dialog.scss',
})
export class SessionNotesDialog {
  protected readonly notes = inject<SessionNotes>(MAT_DIALOG_DATA);
}
