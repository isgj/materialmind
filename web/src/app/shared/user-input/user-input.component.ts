import { Component, computed, effect, input, output, signal, untracked } from '@angular/core';
import { FormField, applyEach, disabled, form, maxLength } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatRadioChange, MatRadioModule } from '@angular/material/radio';

import { UserQuestion } from '../../core/models';
import { UserInputState, UserInputSubmission } from './user-input.models';

interface AnswerDraft {
  questionId: string;
  optionId: string;
  text: string;
}

@Component({
  selector: 'app-user-input',
  imports: [
    FormField,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    MatRadioModule,
  ],
  templateUrl: './user-input.component.html',
  styleUrl: './user-input.component.scss',
})
export class UserInputComponent {
  readonly request = input.required<UserInputState>();
  readonly submitted = output<UserInputSubmission>();

  private readonly model = signal({ answers: [] as AnswerDraft[] });
  protected readonly answersForm = form(this.model, (path) => {
    applyEach(path.answers, (answer) => {
      maxLength(answer.text, 2000);
      disabled(answer.optionId, { when: () => this.request().status !== 'pending' });
      disabled(answer.text, { when: () => this.request().status !== 'pending' });
    });
  });
  protected readonly canSubmit = computed(() => {
    if (this.request().status !== 'pending' || this.answersForm().invalid()) {
      return false;
    }
    const drafts = new Map(this.model().answers.map((answer) => [answer.questionId, answer]));
    return this.request().questions.every((question) => {
      const answer = drafts.get(question.id);
      return !!answer && (answer.optionId !== '' || answer.text.trim() !== '');
    });
  });

  private activeRequestId: string | null = null;

  constructor() {
    effect(() => {
      const request = this.request();
      if (request.id === this.activeRequestId) {
        return;
      }
      this.activeRequestId = request.id;
      untracked(() => {
        this.model.set({
          answers: request.questions.map((question) => ({
            questionId: question.id,
            optionId: '',
            text: '',
          })),
        });
      });
    });
  }

  protected chooseOption(questionId: string, change: MatRadioChange): void {
    this.updateDraft(questionId, {
      optionId: String(change.value),
      text: '',
    });
  }

  protected chooseCustom(questionId: string): void {
    this.updateDraft(questionId, { optionId: '' });
  }

  protected answerFor(question: UserQuestion): string {
    return (
      this.request().answers?.find((answer) => answer.questionId === question.id)?.answer ??
      'No answer recorded'
    );
  }

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (!this.canSubmit()) {
      return;
    }
    this.submitted.emit({
      id: this.request().id,
      answers: this.model().answers.map((answer) =>
        answer.optionId
          ? { questionId: answer.questionId, optionId: answer.optionId }
          : { questionId: answer.questionId, text: answer.text.trim() },
      ),
    });
  }

  private updateDraft(questionId: string, changes: Partial<AnswerDraft>): void {
    this.model.update((current) => ({
      answers: current.answers.map((answer) =>
        answer.questionId === questionId ? { ...answer, ...changes } : answer,
      ),
    }));
  }
}
