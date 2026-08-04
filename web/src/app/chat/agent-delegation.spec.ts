import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AgentDelegationComponent } from './agent-delegation';
import { AgentDelegation } from './chat-timeline';

describe('AgentDelegationComponent', () => {
  let fixture: ComponentFixture<AgentDelegationComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [AgentDelegationComponent] });
    fixture = TestBed.createComponent(AgentDelegationComponent);
  });

  it('opens while running and closes once the delegated task completes', () => {
    const running: AgentDelegation = {
      id: 'delegation-1',
      name: 'workspace_explorer',
      label: 'Workspace explorer',
      task: 'Locate the session store.',
      status: 'running',
      timeline: [],
    };
    fixture.componentRef.setInput('delegation', running);
    fixture.detectChanges();

    const header = fixture.nativeElement.querySelector('mat-expansion-panel-header');
    const task = fixture.nativeElement.querySelector('.delegation-heading > span');
    expect(header.getAttribute('aria-expanded')).toBe('true');
    expect(task.getAttribute('title')).toBe('Locate the session store.');
    expect(fixture.nativeElement.textContent).toContain('Starting delegated work');

    fixture.componentRef.setInput('delegation', {
      ...running,
      status: 'complete',
      result: 'Found `internal/store`.',
    } satisfies AgentDelegation);
    fixture.detectChanges();

    expect(header.getAttribute('aria-expanded')).toBe('false');
    header.click();
    fixture.detectChanges();
    expect(header.getAttribute('aria-expanded')).toBe('true');
    expect(fixture.nativeElement.textContent).toContain('Found');
  });

  it('bubbles nested approval state to the delegation panel', () => {
    fixture.componentRef.setInput('delegation', {
      id: 'delegation-1',
      name: 'code_reviewer',
      label: 'Code reviewer',
      task: 'Review the store.',
      status: 'complete',
      timeline: [
        {
          kind: 'activity',
          id: 'activity-1',
          running: true,
          steps: [
            {
              id: 'read-1',
              toolName: 'read_file',
              label: 'Read internal/store/store.go',
              icon: 'description',
              status: 'approval_required',
              approval: {
                id: 'approval-1',
                kind: 'filesystem_access',
                prompt: 'Allow this file read?',
                icon: 'description',
                targetLabel: 'Path',
                target: 'internal/store/store.go',
                status: 'pending',
              },
            },
          ],
        },
      ],
    } satisfies AgentDelegation);
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('app-agent-activity')).not.toBeNull();
    expect(fixture.nativeElement.textContent).toContain('Read internal/store/store.go');
    expect(fixture.nativeElement.querySelector('.delegation-state .waiting')).not.toBeNull();
  });

  it('shows completion instead of stale nested running activity', () => {
    fixture.componentRef.setInput('delegation', {
      id: 'delegation-1',
      name: 'code_reviewer',
      label: 'Code reviewer',
      task: 'Review the store.',
      status: 'complete',
      result: 'Review complete.',
      timeline: [
        {
          kind: 'activity',
          id: 'activity-1',
          running: true,
          steps: [
            {
              id: 'read-1',
              toolName: 'read_file',
              label: 'Read internal/store/store.go',
              icon: 'description',
              status: 'running',
            },
          ],
        },
      ],
    } satisfies AgentDelegation);
    fixture.detectChanges();

    const state = fixture.nativeElement.querySelector('.delegation-state') as HTMLElement;
    expect(state.querySelector('mat-spinner')).toBeNull();
    expect(state.querySelector('mat-icon')?.textContent?.trim()).toBe('check_circle');
  });

  it('scrolls its activity container when a new subagent step is added', () => {
    const running: AgentDelegation = {
      id: 'delegation-1',
      name: 'code_reviewer',
      label: 'Code reviewer',
      task: 'Review the store.',
      status: 'running',
      timeline: [
        {
          kind: 'activity',
          id: 'activity-1',
          running: true,
          steps: [
            {
              id: 'read-1',
              toolName: 'read_file',
              label: 'Read store.go',
              icon: 'description',
              status: 'complete',
            },
          ],
        },
      ],
    };
    fixture.componentRef.setInput('delegation', running);
    fixture.detectChanges();

    const body = fixture.nativeElement.querySelector('.delegation-body') as HTMLElement;
    Object.defineProperty(body, 'scrollHeight', { configurable: true, value: 480 });
    body.scrollTop = 0;
    fixture.componentRef.setInput('delegation', {
      ...running,
      timeline: [
        {
          ...running.timeline[0],
          kind: 'activity',
          steps: [
            ...(running.timeline[0].kind === 'activity' ? running.timeline[0].steps : []),
            {
              id: 'grep-1',
              toolName: 'grep',
              label: 'Search session state',
              icon: 'search',
              status: 'running',
            },
          ],
        },
      ],
    } satisfies AgentDelegation);
    fixture.detectChanges();

    expect(body.scrollTop).toBe(480);
  });

  it('scrolls its activity container when an existing step starts waiting for approval', () => {
    const running: AgentDelegation = {
      id: 'delegation-1',
      name: 'code_reviewer',
      label: 'Code reviewer',
      task: 'Review the store.',
      status: 'running',
      timeline: [
        {
          kind: 'activity',
          id: 'activity-1',
          running: true,
          steps: [
            {
              id: 'command-1',
              toolName: 'run_command',
              label: 'Run jj diff',
              icon: 'terminal',
              status: 'running',
            },
          ],
        },
      ],
    };
    const activity = running.timeline[0];
    if (activity.kind !== 'activity') {
      throw new Error('expected an activity timeline entry');
    }
    fixture.componentRef.setInput('delegation', running);
    fixture.detectChanges();

    const body = fixture.nativeElement.querySelector('.delegation-body') as HTMLElement;
    Object.defineProperty(body, 'scrollHeight', { configurable: true, value: 720 });
    body.scrollTop = 0;
    fixture.componentRef.setInput('delegation', {
      ...running,
      timeline: [
        {
          kind: 'activity',
          id: 'activity-1',
          running: true,
          steps: [
            {
              ...activity.steps[0],
              status: 'approval_required',
              approval: {
                id: 'approval-1',
                kind: 'command_run',
                prompt: 'Allow this command to run?',
                icon: 'terminal',
                targetLabel: 'Command',
                target: 'jj diff',
                status: 'pending',
              },
            },
          ],
        },
      ],
    } satisfies AgentDelegation);
    fixture.detectChanges();

    expect(body.scrollTop).toBe(720);
  });
});
