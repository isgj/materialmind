import { JsonPipe } from '@angular/common';
import { Component, computed, inject, input } from '@angular/core';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { DomSanitizer, SafeUrl } from '@angular/platform-browser';

import { MarkdownPipe } from '../../shared/markdown.pipe';
import { MCPActivityDetails, MCPContentItem } from '../chat-timeline';
import { McpAppView } from './mcp-app-view';

@Component({
  selector: 'app-mcp-tool-details',
  imports: [
    JsonPipe,
    MarkdownPipe,
    McpAppView,
    MatExpansionModule,
    MatIconModule,
    MatProgressBarModule,
  ],
  templateUrl: './mcp-tool-details.html',
  styleUrl: './mcp-tool-details.scss',
})
export class McpToolDetails {
  readonly sessionId = input('');
  readonly details = input.required<MCPActivityDetails>();
  readonly arguments = input<Record<string, unknown>>({});

  private readonly sanitizer = inject(DomSanitizer);

  protected readonly hasArguments = computed(() => Object.keys(this.arguments()).length > 0);
  protected readonly progressValue = computed(() => {
    const { progress, total } = this.details();
    if (progress === undefined || total === undefined || total <= 0) {
      return 0;
    }
    return Math.min(100, Math.max(0, (progress / total) * 100));
  });
  protected readonly determinateProgress = computed(() => {
    const { progress, total } = this.details();
    return progress !== undefined && total !== undefined && total > 0;
  });

  protected text(item: MCPContentItem): string {
    return typeof item['text'] === 'string' ? item['text'] : '';
  }

  protected title(item: MCPContentItem): string {
    return (
      stringValue(item['title']) ||
      stringValue(item['name']) ||
      stringValue(item['uri']) ||
      'MCP resource'
    );
  }

  protected webLink(item: MCPContentItem): string | null {
    const uri = stringValue(item['uri']);
    try {
      const parsed = new URL(uri);
      return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? uri : null;
    } catch {
      return null;
    }
  }

  protected mediaUrl(item: MCPContentItem, kind: 'image' | 'audio'): SafeUrl | null {
    return this.trustedDataUrl(stringValue(item['mimeType']), stringValue(item['data']), kind);
  }

  protected embeddedResource(item: MCPContentItem): Record<string, unknown> | null {
    return recordValue(item['resource']);
  }

  protected embeddedMediaUrl(item: MCPContentItem, kind: 'image' | 'audio'): SafeUrl | null {
    const resource = this.embeddedResource(item);
    return this.trustedDataUrl(
      stringValue(resource?.['mimeType']),
      stringValue(resource?.['blob']),
      kind,
    );
  }

  protected embeddedText(item: MCPContentItem): string {
    return stringValue(this.embeddedResource(item)?.['text']);
  }

  protected embeddedURI(item: MCPContentItem): string {
    return stringValue(this.embeddedResource(item)?.['uri']);
  }

  protected logText(data: unknown): string | null {
    return typeof data === 'string' ? data : null;
  }

  private trustedDataUrl(mimeType: string, data: string, kind: 'image' | 'audio'): SafeUrl | null {
    if (!data || !mimeType.toLowerCase().startsWith(`${kind}/`)) {
      return null;
    }
    return this.sanitizer.bypassSecurityTrustUrl(`data:${mimeType};base64,${data}`);
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}
