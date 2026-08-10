import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AgentNoteComponent } from './agent-note.component';

describe('AgentNoteComponent', () => {
  let fixture: ComponentFixture<AgentNoteComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({ imports: [AgentNoteComponent] }).compileComponents();
    fixture = TestBed.createComponent(AgentNoteComponent);
    fixture.componentRef.setInput('text', '**Inspecting** the project.');
    await fixture.whenStable();
  });

  it('renders an unstructured note as a single thought label', () => {
    const element = fixture.nativeElement as HTMLElement;

    expect(element.querySelector('mat-icon')?.textContent?.trim()).toBe('psychology');
    expect(element.querySelector('.agent-thought-label')?.textContent).toContain(
      'Inspecting the project.',
    );
    expect(element.querySelector('mat-expansion-panel')).toBeNull();
  });

  it('renders every completed thought as its own collapsed panel', async () => {
    fixture.componentRef.setInput(
      'text',
      [
        '**Evaluating test structure**',
        '',
        'The current tests cover the main behavior.',
        '',
        '**Formulating review comments**',
        '',
        'I am preparing the findings.',
        '',
        '**Adjusting test severity**',
        '',
        'The final severity should reflect the impact.',
      ].join('\n'),
    );
    await fixture.whenStable();

    const element = fixture.nativeElement as HTMLElement;
    const headers = Array.from(element.querySelectorAll<HTMLElement>('mat-expansion-panel-header'));
    const headings = Array.from(element.querySelectorAll('.agent-thought-title'), (heading) =>
      heading.textContent?.trim(),
    );

    expect(headers).toHaveLength(3);
    expect(headings).toEqual([
      'Evaluating test structure',
      'Formulating review comments',
      'Adjusting test severity',
    ]);
    expect(headers.map((header) => header.getAttribute('aria-expanded'))).toEqual([
      'false',
      'false',
      'false',
    ]);

    headers[1].click();
    await fixture.whenStable();

    const details = Array.from(element.querySelectorAll('.agent-thought-detail'), (detail) =>
      detail.textContent?.trim(),
    );
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');
    expect(details).toEqual([
      'The current tests cover the main behavior.',
      'I am preparing the findings.',
      'The final severity should reflect the impact.',
    ]);
    expect(details.join(' ')).not.toContain('Formulating review comments');
  });

  it('opens each thought while the note is active and collapses them when it completes', async () => {
    fixture.componentRef.setInput(
      'text',
      '**Evaluating test structure**\n\nThe current tests cover the main behavior.',
    );
    fixture.componentRef.setInput('active', true);
    await fixture.whenStable();

    const element = fixture.nativeElement as HTMLElement;
    const header = element.querySelector('mat-expansion-panel-header') as HTMLElement;
    expect(header.getAttribute('aria-expanded')).toBe('true');
    expect(element.querySelector('.agent-thought-detail')?.textContent).toContain(
      'The current tests cover the main behavior.',
    );
    expect(element.querySelector('.agent-thought-detail')?.textContent).not.toContain(
      'Evaluating test structure',
    );

    fixture.componentRef.setInput('active', false);
    await fixture.whenStable();

    expect(header.getAttribute('aria-expanded')).toBe('false');
  });
});
