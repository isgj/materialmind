import { Component, computed, input } from '@angular/core';

import { FileChangeOperation } from '../tool-approval/tool-approval.models';
import { highlightCode, syntaxLanguageForPath } from './syntax-highlight';

export type DiffLineKind = 'added' | 'removed' | 'context' | 'hunk' | 'note';

export interface DiffLine {
  kind: DiffLineKind;
  content: string;
  symbol: string;
  oldLine: number | null;
  newLine: number | null;
}

interface HighlightedDiffLine extends DiffLine {
  highlightedContent: string | null;
}

@Component({
  selector: 'app-diff-preview',
  imports: [],
  templateUrl: './diff-preview.component.html',
  styleUrl: './diff-preview.component.scss',
})
export class DiffPreviewComponent {
  readonly path = input.required<string>();
  readonly diff = input.required<string>();
  readonly operation = input<FileChangeOperation>('update');

  protected readonly lines = computed(() => parseUnifiedDiff(this.diff()));
  protected readonly highlightedLines = computed<HighlightedDiffLine[]>(() => {
    const language = syntaxLanguageForPath(this.path());
    return this.lines().map((line) => ({
      ...line,
      highlightedContent:
        language && line.kind !== 'hunk' && line.kind !== 'note'
          ? highlightCode(line.content, language)
          : null,
    }));
  });
  protected readonly additions = computed(
    () => this.lines().filter((line) => line.kind === 'added').length,
  );
  protected readonly deletions = computed(
    () => this.lines().filter((line) => line.kind === 'removed').length,
  );
  protected readonly operationLabel = computed(() => {
    switch (this.operation()) {
      case 'create':
        return 'Created';
      case 'delete':
        return 'Deleted';
      default:
        return 'Updated';
    }
  });
}

export function parseUnifiedDiff(diff: string): DiffLine[] {
  const result: DiffLine[] = [];
  let oldLine = 0;
  let newLine = 0;
  let inHunk = false;

  for (const rawLine of diff.split('\n')) {
    if (rawLine.startsWith('@@')) {
      const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(rawLine);
      if (match) {
        oldLine = Number(match[1]);
        newLine = Number(match[2]);
      }
      inHunk = true;
      result.push({ kind: 'hunk', content: rawLine, symbol: '', oldLine: null, newLine: null });
      continue;
    }
    if (!inHunk) {
      continue;
    }
    if (rawLine.startsWith('\\')) {
      result.push({ kind: 'note', content: rawLine, symbol: '', oldLine: null, newLine: null });
      continue;
    }
    if (rawLine.startsWith('+')) {
      result.push({
        kind: 'added',
        content: rawLine.slice(1),
        symbol: '+',
        oldLine: null,
        newLine,
      });
      newLine++;
      continue;
    }
    if (rawLine.startsWith('-')) {
      result.push({
        kind: 'removed',
        content: rawLine.slice(1),
        symbol: '-',
        oldLine,
        newLine: null,
      });
      oldLine++;
      continue;
    }
    if (rawLine.startsWith(' ')) {
      result.push({
        kind: 'context',
        content: rawLine.slice(1),
        symbol: ' ',
        oldLine,
        newLine,
      });
      oldLine++;
      newLine++;
    }
  }
  return result;
}
