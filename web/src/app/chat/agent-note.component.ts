import { Component, computed, input } from '@angular/core';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';
import { marked } from 'marked';
import type { Token, Tokens } from 'marked';

import { MarkdownPipe } from '../shared/markdown.pipe';

interface AgentThought {
  title: string;
  detail: string;
}

function tokenText(token: Token): string {
  if ('tokens' in token && Array.isArray(token.tokens)) {
    return token.tokens.map(tokenText).join(' ').replace(/\s+/g, ' ').trim();
  }
  if ('text' in token && typeof token.text === 'string') {
    return token.text.replace(/\s+/g, ' ').trim();
  }
  return '';
}

function standaloneStrongHeading(token: Token): string | null {
  if (token.type !== 'paragraph') {
    return null;
  }
  const inlineTokens = (token as Tokens.Paragraph).tokens.filter(
    (inlineToken) => inlineToken.type !== 'text' || tokenText(inlineToken).length > 0,
  );
  if (inlineTokens.length !== 1 || inlineTokens[0].type !== 'strong') {
    return null;
  }
  return tokenText(inlineTokens[0]) || null;
}

function noteThoughts(markdown: string): readonly AgentThought[] {
  const tokens = marked.lexer(markdown, { breaks: true, gfm: true });
  const thoughts: AgentThought[] = [];
  const preamble: Token[] = [];
  let current: AgentThought | null = null;

  for (const token of tokens) {
    const heading = token.type === 'heading' ? tokenText(token) : standaloneStrongHeading(token);
    if (heading) {
      if (current) {
        thoughts.push({ ...current, detail: current.detail.trim() });
      }
      current = {
        title: heading,
        detail: thoughts.length === 0 ? preamble.map((item) => item.raw).join('') : '',
      };
      preamble.length = 0;
    } else if (current) {
      current.detail += token.raw;
    } else {
      preamble.push(token);
    }
  }

  if (current) {
    thoughts.push({ ...current, detail: current.detail.trim() });
    return thoughts;
  }

  const firstContentIndex = tokens.findIndex(
    (token) => token.type !== 'space' && token.type !== 'def',
  );
  if (firstContentIndex < 0) {
    return [];
  }
  const firstContent = tokens[firstContentIndex];
  if (firstContent.type !== 'paragraph') {
    return [{ title: 'Agent note', detail: markdown.trim() }];
  }
  return [
    {
      title: tokenText(firstContent) || 'Agent note',
      detail: tokens
        .filter((_, index) => index !== firstContentIndex)
        .map((token) => token.raw)
        .join('')
        .trim(),
    },
  ];
}

@Component({
  selector: 'app-agent-note',
  imports: [MarkdownPipe, MatExpansionModule, MatIconModule, MatTooltipModule],
  template: `
    <article class="agent-note" aria-label="Agent note">
      <span class="agent-note-marker" aria-hidden="true" matTooltip="Agent note">
        <mat-icon>psychology</mat-icon>
      </span>
      <mat-accordion class="agent-thoughts" multi aria-label="Agent thoughts">
        @for (thought of thoughts(); track $index) {
          @if (thought.detail) {
            <mat-expansion-panel class="agent-thought-panel" [expanded]="active()">
              <mat-expansion-panel-header>
                <mat-panel-title class="agent-thought-title">
                  <span>{{ thought.title }}</span>
                </mat-panel-title>
              </mat-expansion-panel-header>
              <div
                class="agent-thought-detail markdown-body"
                [innerHTML]="thought.detail | markdown"
              ></div>
            </mat-expansion-panel>
          } @else {
            <div class="agent-thought-label">{{ thought.title }}</div>
          }
        }
      </mat-accordion>
    </article>
  `,
  styles: `
    :host {
      display: block;
      width: 100%;
    }

    .agent-note {
      display: grid;
      min-width: 0;
      grid-template-columns: 32px minmax(0, 1fr);
      align-items: start;
      gap: 12px;
      color: var(--mat-sys-on-surface);
    }

    .agent-note-marker {
      display: grid;
      width: 32px;
      height: 32px;
      margin-top: 10px;
      border-radius: 50%;
      background: var(--mat-sys-primary-container);
      color: var(--mat-sys-on-primary-container);
      place-items: center;
    }

    .agent-note-marker mat-icon {
      width: 18px;
      height: 18px;
      font-size: 18px;
    }

    .agent-thoughts {
      display: grid;
      min-width: 0;
      gap: 2px;
    }

    .agent-thought-panel {
      min-width: 0;
      --mat-expansion-container-background-color: transparent;
      --mat-expansion-container-elevation-shadow: none;
      --mat-expansion-container-shape: 0;
    }

    .agent-thought-panel.mat-expansion-panel-spacing {
      margin: 0;
    }

    .agent-thought-panel > .mat-expansion-panel-header {
      box-sizing: border-box;
      min-height: 52px;
      height: auto;
      padding: 8px 30px 8px 16px;
    }

    .agent-thought-panel > .mat-expansion-panel-header:hover,
    .agent-thought-panel > .mat-expansion-panel-header:focus-visible {
      background: var(--mat-sys-surface-container-low);
    }

    .agent-thought-title {
      min-width: 0;
      margin-inline-end: 16px;
      color: var(--mat-sys-on-surface);
      font: var(--mat-sys-title-small);
      font-weight: 500;
    }

    .agent-thought-title > span {
      min-width: 0;
      overflow-wrap: anywhere;
    }

    .agent-thought-label {
      display: flex;
      box-sizing: border-box;
      min-height: 52px;
      align-items: center;
      padding: 8px 16px;
      color: var(--mat-sys-on-surface);
      font: var(--mat-sys-title-small);
      font-weight: 500;
      overflow-wrap: anywhere;
    }

    .agent-thought-detail {
      margin-inline: -8px;
    }

    .markdown-body {
      min-width: 0;
      font: var(--mat-sys-body-medium);
      line-height: 1.58;
      overflow-wrap: anywhere;
    }

    @media (max-width: 640px) {
      .agent-note {
        grid-template-columns: 28px minmax(0, 1fr);
        gap: 8px;
      }

      .agent-note-marker {
        width: 28px;
        height: 28px;
        margin-top: 12px;
      }

      .agent-note-marker mat-icon {
        width: 17px;
        height: 17px;
        font-size: 17px;
      }

      .agent-thought-panel > .mat-expansion-panel-header {
        padding-inline: 12px 26px;
      }

      .agent-thought-label {
        padding-inline: 12px;
      }
    }
  `,
})
export class AgentNoteComponent {
  readonly text = input.required<string>();
  readonly active = input(false);
  protected readonly thoughts = computed(() => noteThoughts(this.text()));
}
