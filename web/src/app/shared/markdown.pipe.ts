import { Pipe, PipeTransform, SecurityContext, inject } from '@angular/core';
import { DomSanitizer } from '@angular/platform-browser';
import { Renderer, Tokens, marked } from 'marked';

class MarkdownRenderer extends Renderer {
  override link(token: Tokens.Link): string {
    const link = super.link(token);
    return link.startsWith('<a ')
      ? link.replace('<a ', '<a target="_blank" rel="noopener noreferrer" ')
      : link;
  }
}

@Pipe({
  name: 'markdown',
})
export class MarkdownPipe implements PipeTransform {
  private readonly sanitizer = inject(DomSanitizer);

  transform(value: string | null | undefined): string {
    if (!value) {
      return '';
    }

    const html = marked.parse(value, {
      async: false,
      breaks: true,
      gfm: true,
      renderer: new MarkdownRenderer(),
    });

    return this.sanitizer.sanitize(SecurityContext.HTML, html) ?? '';
  }
}
