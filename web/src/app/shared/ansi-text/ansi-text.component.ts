import { Component, computed, input } from '@angular/core';

import { parseAnsiText } from './ansi-text';

@Component({
  selector: 'app-ansi-text',
  templateUrl: './ansi-text.component.html',
  styleUrl: './ansi-text.component.scss',
})
export class AnsiTextComponent {
  readonly value = input.required<string>();

  protected readonly segments = computed(() => parseAnsiText(this.value()));
}
