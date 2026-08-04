import { ComponentFixture, TestBed } from '@angular/core/testing';

import { DiffPreviewComponent, parseUnifiedDiff } from './diff-preview.component';
import { highlightCode, syntaxLanguageForPath } from './syntax-highlight';

const diff = [
  '--- a/main.go',
  '+++ b/main.go',
  '@@ -2,3 +2,3 @@',
  ' context',
  '-const greeting = "hello"',
  '+const greeting = "hello, world"',
  ' tail',
  '',
].join('\n');

describe('DiffPreviewComponent', () => {
  let fixture: ComponentFixture<DiffPreviewComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [DiffPreviewComponent] });
    fixture = TestBed.createComponent(DiffPreviewComponent);
    fixture.componentRef.setInput('path', 'main.go');
    fixture.componentRef.setInput('diff', diff);
  });

  it('parses hunk line numbers and change kinds', () => {
    expect(parseUnifiedDiff(diff)).toEqual([
      { kind: 'hunk', content: '@@ -2,3 +2,3 @@', symbol: '', oldLine: null, newLine: null },
      { kind: 'context', content: 'context', symbol: ' ', oldLine: 2, newLine: 2 },
      {
        kind: 'removed',
        content: 'const greeting = "hello"',
        symbol: '-',
        oldLine: 3,
        newLine: null,
      },
      {
        kind: 'added',
        content: 'const greeting = "hello, world"',
        symbol: '+',
        oldLine: null,
        newLine: 3,
      },
      { kind: 'context', content: 'tail', symbol: ' ', oldLine: 4, newLine: 4 },
    ]);
  });

  it('renders a scrollable diff with additions and deletions', async () => {
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('.diff-lines')?.getAttribute('aria-label')).toBe('Diff for main.go');
    expect(root.querySelectorAll('.diff-line.added')).toHaveLength(1);
    expect(root.querySelectorAll('.diff-line.removed')).toHaveLength(1);
    expect(root.querySelector('.diff-stats')?.getAttribute('aria-label')).toBe(
      '1 additions and 1 deletions',
    );
    expect(root.querySelector('.diff-title')?.textContent).toContain('main.go');
    expect(root.querySelector('.diff-title')?.textContent).toContain('Updated');
    expect(root.textContent).toContain('const greeting = "hello, world"');
    expect(root.querySelector('.diff-line.added .hljs-keyword')?.textContent).toBe('const');
    expect(root.querySelector('.diff-line.added .hljs-string')?.textContent).toBe('"hello, world"');
  });

  it('maps known file paths to explicit languages', () => {
    expect(syntaxLanguageForPath('internal/server.go')).toBe('go');
    expect(syntaxLanguageForPath('web/src/app.ts')).toBe('typescript');
    expect(syntaxLanguageForPath('notes.txt')).toBeNull();
  });

  it('returns escaped Highlight.js markup', () => {
    const highlighted = highlightCode('<script>unsafe()</script>', 'go');

    expect(highlighted).toContain('&lt;script&gt;');
    expect(highlighted).not.toContain('<script>');
  });

  it('renders diff content as text', async () => {
    fixture.componentRef.setInput(
      'diff',
      '--- a/main.go\n+++ b/main.go\n@@ -0,0 +1 @@\n+<script>unsafe()</script>\n',
    );
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('script')).toBeNull();
    expect(root.querySelector('.diff-line code')?.textContent).toBe('<script>unsafe()</script>');
  });

  it('identifies an empty file operation without inventing diff lines', async () => {
    fixture.componentRef.setInput('path', 'empty.txt');
    fixture.componentRef.setInput('operation', 'create');
    fixture.componentRef.setInput('diff', '--- /dev/null\n+++ b/empty.txt\n');
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('.diff-title')?.textContent).toContain('empty.txt');
    expect(root.querySelector('.diff-title')?.textContent).toContain('Created');
    expect(root.querySelector('.diff-empty')?.textContent).toContain('Empty file');
  });
});
