import {
  Component,
  DestroyRef,
  ElementRef,
  computed,
  effect,
  inject,
  input,
  signal,
  viewChild,
} from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { DomSanitizer, SafeHtml } from '@angular/platform-browser';

import { ApiService, errorMessage } from '../../core/api.service';
import { McpResourceContent } from '../../core/models';
import { MCPActivityDetails } from '../chat-timeline';

const appProtocolVersion = '2026-01-26';
const defaultAppHeight = 320;
const maximumAppHeight = 640;

@Component({
  selector: 'app-mcp-app-view',
  host: {
    '(window:message)': 'receiveMessage($event)',
  },
  imports: [MatIconModule, MatProgressSpinnerModule],
  template: `
    <section class="app-shell" aria-label="MCP App">
      @if (loading()) {
        <div class="app-state">
          <mat-spinner diameter="24" aria-label="Loading MCP App"></mat-spinner>
        </div>
      } @else if (error()) {
        <div class="app-state app-error" role="status">
          <mat-icon>web_asset_off</mat-icon>
          <span>{{ error() }}</span>
        </div>
      } @else if (document(); as source) {
        <iframe
          #frame
          title="MCP App"
          sandbox="allow-scripts"
          [srcdoc]="source"
          [style.height.px]="height()"
        ></iframe>
      }
    </section>
  `,
  styles: `
    .app-shell {
      overflow: hidden;
      border: 1px solid var(--mat-sys-outline-variant);
      border-radius: 8px;
      background: var(--mat-sys-surface-container-low);
    }

    iframe {
      display: block;
      width: 100%;
      border: 0;
      background: transparent;
    }

    .app-state {
      min-height: 160px;
      display: grid;
      place-content: center;
      justify-items: center;
      gap: 8px;
      padding: 16px;
    }

    .app-error {
      color: var(--mat-sys-error);
    }
  `,
})
export class McpAppView {
  readonly sessionId = input.required<string>();
  readonly details = input.required<MCPActivityDetails>();
  readonly arguments = input<Record<string, unknown>>({});

  private readonly api = inject(ApiService);
  private readonly sanitizer = inject(DomSanitizer);
  private readonly destroyRef = inject(DestroyRef);
  private readonly frame = viewChild<ElementRef<HTMLIFrameElement>>('frame');
  private loadGeneration = 0;
  private resourceKey = '';

  protected readonly loading = signal(false);
  protected readonly error = signal('');
  protected readonly rawDocument = signal('');
  protected readonly initialized = signal(false);
  protected readonly document = computed<SafeHtml | null>(() => {
    const value = this.rawDocument();
    return value ? this.sanitizer.bypassSecurityTrustHtml(value) : null;
  });
  protected readonly height = signal(defaultAppHeight);

  constructor() {
    effect(() => {
      const sessionId = this.sessionId();
      const details = this.details();
      const key = `${sessionId}\u0000${details.serverId}\u0000${details.uiResourceUri ?? ''}`;
      if (key === this.resourceKey) {
        return;
      }
      this.resourceKey = key;
      void this.load(sessionId, details.serverId, details.uiResourceUri ?? '');
    });
    effect(() => {
      this.details();
      this.arguments();
      if (this.initialized()) {
        queueMicrotask(() => this.sendToolContext());
      }
    });
    this.destroyRef.onDestroy(() => {
      this.loadGeneration++;
    });
  }

  protected receiveMessage(event: MessageEvent): void {
    const target = this.frame()?.nativeElement.contentWindow;
    if (!target || event.source !== target || !isJSONRPCMessage(event.data)) {
      return;
    }
    const message = event.data;
    if (message['method'] === 'ui/initialize' && message['id'] !== undefined) {
      target.postMessage(
        {
          jsonrpc: '2.0',
          id: message['id'],
          result: {
            protocolVersion: appProtocolVersion,
            hostInfo: { name: 'MaterialMind', title: 'MaterialMind', version: 'development' },
            hostCapabilities: {
              sandbox: {},
            },
            hostContext: {
              theme: currentTheme(),
              displayMode: 'inline',
              availableDisplayModes: ['inline'],
              containerDimensions: { maxWidth: 1200, maxHeight: maximumAppHeight },
              locale: navigator.language,
              timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
              userAgent: 'MaterialMind',
              platform: 'web',
              deviceCapabilities: {
                touch: navigator.maxTouchPoints > 0,
                hover: window.matchMedia('(hover: hover)').matches,
              },
            },
          },
        },
        '*',
      );
      return;
    }
    if (message['method'] === 'ui/notifications/initialized') {
      this.initialized.set(true);
      return;
    }
    if (message['method'] === 'ui/notifications/size-changed') {
      const params = recordValue(message['params']);
      const requested = numberValue(params?.['height']);
      if (requested !== null) {
        this.height.set(Math.min(maximumAppHeight, Math.max(120, Math.ceil(requested))));
      }
      return;
    }
    if (message['id'] !== undefined) {
      target.postMessage(
        {
          jsonrpc: '2.0',
          id: message['id'],
          error: { code: -32601, message: 'Method is not enabled by this host' },
        },
        '*',
      );
    }
  }

  private async load(sessionId: string, serverId: string, uri: string): Promise<void> {
    const generation = ++this.loadGeneration;
    this.initialized.set(false);
    this.rawDocument.set('');
    this.error.set('');
    if (!sessionId || !serverId || !uri) {
      return;
    }
    this.loading.set(true);
    try {
      const resource = await this.api.readSessionMcpResource(sessionId, serverId, uri);
      if (generation !== this.loadGeneration) {
        return;
      }
      const content = resource.contents.find((item) => isAppHTML(item));
      if (!content?.text) {
        throw new Error('The MCP App resource did not return HTML');
      }
      this.rawDocument.set(appDocument(content));
    } catch (error) {
      if (generation === this.loadGeneration) {
        this.error.set(errorMessage(error));
      }
    } finally {
      if (generation === this.loadGeneration) {
        this.loading.set(false);
      }
    }
  }

  private sendToolContext(): void {
    if (!this.initialized()) {
      return;
    }
    const target = this.frame()?.nativeElement.contentWindow;
    if (!target) {
      return;
    }
    target.postMessage(
      {
        jsonrpc: '2.0',
        method: 'ui/notifications/tool-input',
        params: { arguments: this.arguments() },
      },
      '*',
    );
    const details = this.details();
    target.postMessage(
      {
        jsonrpc: '2.0',
        method: 'ui/notifications/tool-result',
        params: {
          content: details.content,
          structuredContent: details.structuredContent,
          isError: details.isError,
        },
      },
      '*',
    );
  }
}

function isAppHTML(content: McpResourceContent): boolean {
  return content.mimeType?.toLowerCase().startsWith('text/html') ?? false;
}

function appDocument(content: McpResourceContent): string {
  const csp = contentSecurityPolicy(content.meta);
  const policy = `<meta http-equiv="Content-Security-Policy" content="${escapeAttribute(csp)}">`;
  const html = content.text ?? '';
  if (/<head(?:\s[^>]*)?>/i.test(html)) {
    return html.replace(/<head(\s[^>]*)?>/i, (match) => `${match}${policy}`);
  }
  return `<!doctype html><html><head>${policy}</head><body>${html}</body></html>`;
}

function contentSecurityPolicy(meta: Record<string, unknown> | undefined): string {
  const ui = recordValue(meta?.['ui']);
  const csp = recordValue(ui?.['csp']);
  const connect = validCspSources(csp?.['connectDomains']);
  const resources = validCspSources(csp?.['resourceDomains']);
  const frames = validCspSources(csp?.['frameDomains']);
  const base = validCspSources(csp?.['baseUriDomains']);
  return [
    "default-src 'none'",
    `script-src 'unsafe-inline' ${resources.join(' ')}`.trim(),
    `style-src 'unsafe-inline' ${resources.join(' ')}`.trim(),
    `img-src data: blob: ${resources.join(' ')}`.trim(),
    `font-src ${resources.length ? resources.join(' ') : "'none'"}`,
    `media-src data: blob: ${resources.join(' ')}`.trim(),
    `connect-src ${connect.length ? connect.join(' ') : "'none'"}`,
    `frame-src ${frames.length ? frames.join(' ') : "'none'"}`,
    `base-uri ${base.length ? base.join(' ') : "'none'"}`,
    "form-action 'none'",
    "object-src 'none'",
  ].join('; ');
}

function validCspSources(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) => {
    if (typeof item !== 'string' || /[;\s]/.test(item)) {
      return [];
    }
    return /^https:\/\/(?:\*\.)?[a-z0-9.-]+(?::\d+)?$/i.test(item) ? [item] : [];
  });
}

function currentTheme(): 'light' | 'dark' {
  const configured = document.documentElement.dataset['theme'];
  if (configured === 'light' || configured === 'dark') {
    return configured;
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function isJSONRPCMessage(value: unknown): value is Record<string, unknown> {
  const message = recordValue(value);
  return !!message && message['jsonrpc'] === '2.0' && typeof message['method'] === 'string';
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function numberValue(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function escapeAttribute(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;');
}
