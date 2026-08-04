import { ComponentFixture, TestBed } from '@angular/core/testing';

import { UserInputComponent } from './user-input.component';
import { UserInputState } from './user-input.models';

describe('UserInputComponent', () => {
  let fixture: ComponentFixture<UserInputComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [UserInputComponent] });
    fixture = TestBed.createComponent(UserInputComponent);
  });

  it('requires every question and submits selected and custom answers together', () => {
    fixture.componentRef.setInput('request', pendingRequest());
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    const submitButton = fixture.nativeElement.querySelector(
      '.user-input-actions button',
    ) as HTMLButtonElement;
    expect(submitButton.disabled).toBe(true);

    (fixture.nativeElement.querySelector('input[type="radio"]') as HTMLInputElement).click();
    const textareas = fixture.nativeElement.querySelectorAll(
      'textarea',
    ) as NodeListOf<HTMLTextAreaElement>;
    textareas[1].value = 'Keep the public API unchanged.';
    textareas[1].dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();

    expect(submitButton.disabled).toBe(false);
    submitButton.click();

    expect(submitted).toHaveBeenCalledWith({
      id: 'request-1',
      answers: [
        { questionId: 'format', optionId: 'json' },
        { questionId: 'constraint', text: 'Keep the public API unchanged.' },
      ],
    });
  });

  it('uses a custom response instead of a previously selected option', () => {
    const request = pendingRequest();
    fixture.componentRef.setInput('request', {
      ...request,
      questions: request.questions.slice(0, 1),
    });
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('input[type="radio"]') as HTMLInputElement).click();
    const textarea = fixture.nativeElement.querySelector('textarea') as HTMLTextAreaElement;
    textarea.value = 'Use YAML instead.';
    textarea.dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();
    (
      fixture.nativeElement.querySelector('.user-input-actions button') as HTMLButtonElement
    ).click();

    expect(submitted).toHaveBeenCalledWith({
      id: 'request-1',
      answers: [{ questionId: 'format', text: 'Use YAML instead.' }],
    });
  });

  it('renders accepted answers as a read-only summary', () => {
    fixture.componentRef.setInput('request', {
      ...pendingRequest(),
      status: 'answered',
      answers: [
        { questionId: 'format', optionId: 'json', answer: 'JSON' },
        { questionId: 'constraint', answer: 'Keep the public API unchanged.' },
      ],
    } satisfies UserInputState);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('form')).toBeNull();
    expect(root.textContent).toContain('JSON');
    expect(root.textContent).toContain('Keep the public API unchanged.');
    expect(root.querySelectorAll('.answer-row')).toHaveLength(2);
  });
});

function pendingRequest(): UserInputState {
  return {
    id: 'request-1',
    toolCallId: 'tool-1',
    toolName: 'ask_user',
    status: 'pending',
    questions: [
      {
        id: 'format',
        question: 'Which output format should be used?',
        options: [
          { id: 'json', label: 'JSON', description: 'Machine-readable output.' },
          { id: 'text', label: 'Plain text', description: 'Human-readable output.' },
        ],
      },
      {
        id: 'constraint',
        question: 'Are there any compatibility constraints?',
        options: [],
      },
    ],
  };
}
