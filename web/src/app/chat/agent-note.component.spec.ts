import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AgentNoteComponent } from './agent-note.component';

describe('AgentNoteComponent', () => {
  let fixture: ComponentFixture<AgentNoteComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({ imports: [AgentNoteComponent] }).compileComponents();
    fixture = TestBed.createComponent(AgentNoteComponent);
    fixture.componentRef.setInput('text', '**Inspecting** the project.');
    fixture.detectChanges();
  });

  it('renders the note as Markdown outside an activity step', () => {
    const element = fixture.nativeElement as HTMLElement;

    expect(element.querySelector('mat-icon')?.textContent?.trim()).toBe('psychology');
    expect(element.querySelector('.markdown-body strong')?.textContent).toBe('Inspecting');
  });
});
