import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MAT_DIALOG_DATA } from '@angular/material/dialog';

import { SessionNotes } from '../../core/models';

import { SessionNotesDialog } from './session-notes-dialog';

describe('SessionNotesDialog', () => {
  let component: SessionNotesDialog;
  let fixture: ComponentFixture<SessionNotesDialog>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [SessionNotesDialog],
      providers: [
        {
          provide: MAT_DIALOG_DATA,
          useValue: {
            sessionId: 'session-1',
            content: '# Decisions\n\n- Keep the API stable',
            revision: 3,
            updatedAt: '2026-08-03T12:00:00Z',
          } satisfies SessionNotes,
        },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(SessionNotesDialog);
    component = fixture.componentInstance;
    await fixture.whenStable();
  });

  it('renders session notes as Markdown', () => {
    const element = fixture.nativeElement as HTMLElement;

    expect(component).toBeTruthy();
    expect(element.querySelector('h1')?.textContent).toBe('Decisions');
    expect(element.querySelector('li')?.textContent).toBe('Keep the API stable');
    expect(element.textContent).toContain('Revision 3');
  });
});
