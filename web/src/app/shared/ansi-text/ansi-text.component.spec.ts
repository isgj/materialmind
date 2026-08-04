import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AnsiTextComponent } from './ansi-text.component';
import { parseAnsiText } from './ansi-text';

describe('AnsiTextComponent', () => {
  let fixture: ComponentFixture<AnsiTextComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [AnsiTextComponent] });
    fixture = TestBed.createComponent(AnsiTextComponent);
  });

  it('parses standard styles and resets them', () => {
    expect(parseAnsiText('\u001b[1;31merror\u001b[0m plain')).toEqual([
      {
        text: 'error',
        foreground: 'var(--ansi-red)',
        background: null,
        bold: true,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
      {
        text: ' plain',
        foreground: null,
        background: null,
        bold: false,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
    ]);
  });

  it('supports indexed and true-color foregrounds and backgrounds', () => {
    expect(
      parseAnsiText('\u001b[38;5;22;48;5;196mindexed\u001b[0m\u001b[38;2;12;34;56mtrue\u001b[0m'),
    ).toEqual([
      {
        text: 'indexed',
        foreground: 'rgb(0 95 0)',
        background: 'rgb(255 0 0)',
        bold: false,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
      {
        text: 'true',
        foreground: 'rgb(12 34 56)',
        background: null,
        bold: false,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
    ]);
  });

  it('does not carry true-color metadata into a later standard color', () => {
    const segments = parseAnsiText('\u001b[38;2;12;34;56mtrue\u001b[0m plain \u001b[31mstandard');

    expect(segments.map(({ text, foreground }) => ({ text, foreground }))).toEqual([
      { text: 'true', foreground: 'rgb(12 34 56)' },
      { text: ' plain ', foreground: null },
      { text: 'standard', foreground: 'var(--ansi-red)' },
    ]);
  });

  it('keeps styles active when reparsing accumulated streamed output', () => {
    expect(parseAnsiText('\u001b[32mfirst second\u001b[0m plain')).toEqual([
      {
        text: 'first second',
        foreground: 'var(--ansi-green)',
        background: null,
        bold: false,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
      {
        text: ' plain',
        foreground: null,
        background: null,
        bold: false,
        dim: false,
        italic: false,
        hidden: false,
        textDecoration: null,
      },
    ]);
  });

  it('removes terminal metadata and incomplete control sequences', () => {
    expect(
      parseAnsiText(
        'before\u001b]8;;https://example.com\u001b\\link\u001b]8;;\u001b\\ after\u001b[31',
      ).map((segment) => segment.text),
    ).toEqual(['beforelink after']);
  });

  it('renders command content as text with ANSI styles', async () => {
    fixture.componentRef.setInput('value', '\u001b[4;91m<script>not markup</script>\u001b[0m');
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    const segment = root.querySelector('span') as HTMLSpanElement;
    expect(root.querySelector('script')).toBeNull();
    expect(segment.textContent).toBe('<script>not markup</script>');
    expect(segment.style.color).toBe('var(--ansi-bright-red)');
    expect(segment.style.textDecorationLine).toBe('underline');
  });
});
