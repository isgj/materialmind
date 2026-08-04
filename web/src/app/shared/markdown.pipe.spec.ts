import { TestBed } from '@angular/core/testing';

import { MarkdownPipe } from './markdown.pipe';

describe('MarkdownPipe', () => {
  let pipe: MarkdownPipe;

  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [MarkdownPipe] });
    pipe = TestBed.inject(MarkdownPipe);
  });

  it('renders GitHub-flavored Markdown', () => {
    const html = pipe.transform('**Important**\n\n- first\n- second\n\n`go test ./...`');

    expect(html).toContain('<strong>Important</strong>');
    expect(html).toContain('<li>first</li>');
    expect(html).toContain('<code>go test ./...</code>');
  });

  it('opens Markdown links in a new browsing context without opener access', () => {
    const html = pipe.transform(
      '[Documentation](https://example.com/docs) and <https://example.com/reference>',
    );
    const container = document.createElement('div');
    container.innerHTML = html;
    const links = Array.from(container.querySelectorAll('a'));

    expect(links).toHaveLength(2);
    for (const link of links) {
      expect(link.target).toBe('_blank');
      expect(link.relList.contains('noopener')).toBe(true);
      expect(link.relList.contains('noreferrer')).toBe(true);
    }
  });

  it('sanitizes HTML emitted by the Markdown parser', () => {
    const html = pipe.transform('<img src="x" onerror="alert(1)"><script>alert(2)</script>');

    expect(html).not.toContain('onerror');
    expect(html).not.toContain('<script');
  });
});
