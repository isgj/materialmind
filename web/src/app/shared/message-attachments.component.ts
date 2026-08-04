import { Component, input, output } from '@angular/core';
import { MatChipsModule } from '@angular/material/chips';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';

import { MessageAttachment } from '../core/models';

export interface AttachmentPreview extends Pick<
  MessageAttachment,
  'id' | 'name' | 'mimeType' | 'size'
> {
  previewUrl?: string;
}

@Component({
  selector: 'app-message-attachments',
  imports: [MatChipsModule, MatIconModule, MatTooltipModule],
  templateUrl: './message-attachments.component.html',
  styleUrl: './message-attachments.component.scss',
})
export class MessageAttachmentsComponent {
  readonly attachments = input.required<readonly AttachmentPreview[]>();
  readonly removable = input(false);
  readonly removed = output<string>();

  protected isImage(attachment: AttachmentPreview): boolean {
    return attachment.mimeType.startsWith('image/');
  }

  protected imageSource(attachment: AttachmentPreview): string {
    return attachment.previewUrl ?? `/api/run-attachments/${attachment.id}`;
  }

  protected fileIcon(attachment: AttachmentPreview): string {
    if (attachment.mimeType === 'application/pdf') {
      return 'picture_as_pdf';
    }
    return 'description';
  }

  protected tooltip(attachment: AttachmentPreview): string {
    return `${attachment.name} · ${formatFileSize(attachment.size)}`;
  }
}

function formatFileSize(size: number): string {
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KiB`;
  }
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}
