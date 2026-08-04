import { ComponentFixture, TestBed } from '@angular/core/testing';

import { ToolApprovalComponent } from './tool-approval.component';

describe('ToolApprovalComponent', () => {
  let fixture: ComponentFixture<ToolApprovalComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [ToolApprovalComponent] });
    fixture = TestBed.createComponent(ToolApprovalComponent);
    fixture.componentRef.setInput('approval', { id: 'approval-1', status: 'pending' });
    fixture.componentRef.setInput('prompt', 'Allow this file edit?');
    fixture.componentRef.setInput('icon', 'edit_note');
    fixture.componentRef.setInput('targetLabel', 'File');
    fixture.componentRef.setInput('target', 'internal/main.go');
  });

  it('shows the action target and emits a refusal reason', async () => {
    const decision = vi.fn();
    fixture.componentInstance.decision.subscribe(decision);
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.textContent).toContain('Allow this file edit?');
    expect(root.textContent).toContain('internal/main.go');
    const reason = root.querySelector('textarea') as HTMLTextAreaElement;
    reason.value = 'Make a smaller change.';
    reason.dispatchEvent(new Event('input', { bubbles: true }));
    await fixture.whenStable();
    const buttons = root.querySelectorAll<HTMLButtonElement>('.approval-actions button');
    buttons[1]?.click();

    expect(decision).toHaveBeenCalledWith({
      id: 'approval-1',
      approved: false,
      reason: 'Make a smaller change.',
    });
  });

  it('can hide a target that is rendered by specialized approval content', async () => {
    fixture.componentRef.setInput('showTarget', false);
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.textContent).toContain('Allow this file edit?');
    expect(root.querySelector('.target-label')).toBeNull();
    expect(root.textContent).not.toContain('internal/main.go');
  });

  it('disables the decision controls while submitting', async () => {
    fixture.componentRef.setInput('approval', { id: 'approval-1', status: 'submitting' });
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    expect((root.querySelector('textarea') as HTMLTextAreaElement).disabled).toBe(true);
    expect(
      [...root.querySelectorAll<HTMLButtonElement>('.approval-actions button')].every(
        (button) => button.disabled,
      ),
    ).toBe(true);
    expect(root.querySelector('mat-spinner')?.getAttribute('aria-label')).toBe(
      'Submitting decision',
    );
  });

  it('keeps long ACP permission choices inside bounded action buttons', async () => {
    const longCommandOption =
      'Allow Commands Starting With `node -e const fs=require("fs"); const command = process.argv.join(" ");`';
    fixture.componentRef.setInput('approval', {
      id: 'approval-1',
      status: 'pending',
      options: [
        { id: 'once', name: 'Allow Once', kind: 'allow_once' },
        { id: 'session', name: 'Allow for Session', kind: 'allow_always' },
        { id: 'command', name: longCommandOption, kind: 'allow_always' },
        { id: 'reject', name: 'Reject', kind: 'reject_once' },
      ],
    });
    await fixture.whenStable();

    const root = fixture.nativeElement as HTMLElement;
    const actions = root.querySelector('.approval-actions');
    const buttons = actions?.querySelectorAll<HTMLButtonElement>('.approval-option-button');
    const longButton = buttons?.[2];

    expect(buttons).toHaveLength(4);
    expect(longButton?.getAttribute('aria-label')).toBe(longCommandOption);
    expect(longButton?.querySelector('.approval-option-label')?.textContent).toBe(
      longCommandOption,
    );
  });

  it('shows the final refusal and reason', async () => {
    fixture.componentRef.setInput('approval', {
      id: 'approval-1',
      status: 'denied',
      reason: 'Do not touch generated files.',
    });
    await fixture.whenStable();

    const status = (fixture.nativeElement as HTMLElement)
      .querySelector('[role="status"]')
      ?.textContent?.replaceAll(/\s+/g, ' ')
      .trim();
    expect(status).toContain('Action refused: Do not touch generated files.');
  });

  it('shows when an approved action starts executing', async () => {
    fixture.componentRef.setInput('approval', {
      id: 'approval-1',
      status: 'executing',
    });
    await fixture.whenStable();

    const status = (fixture.nativeElement as HTMLElement).querySelector('.approval-resolution');
    expect(status?.textContent).toContain('Action running');
    expect(status?.querySelector('mat-spinner')?.getAttribute('aria-label')).toBe('Action running');
  });
});
