import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AgentPlanComponent } from './agent-plan.component';

describe('AgentPlanComponent', () => {
  let fixture: ComponentFixture<AgentPlanComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [AgentPlanComponent],
    }).compileComponents();
    fixture = TestBed.createComponent(AgentPlanComponent);
    fixture.componentRef.setInput('plan', {
      id: 'plan-1',
      entries: [
        { content: 'Inspect the project', priority: 'high', status: 'completed' },
        { content: 'Update the implementation', priority: 'medium', status: 'in_progress' },
        { content: 'Run tests', priority: 'medium', status: 'pending' },
      ],
    });
    fixture.detectChanges();
  });

  it('shows the execution-plan summary and progress in its compact header', () => {
    const element = fixture.nativeElement as HTMLElement;

    expect(element.querySelector('.plan-title')?.textContent).toContain('Project Execution Plan');
    expect(element.querySelector('.progress-label')?.textContent).toContain(
      '1 of 3 tasks completed',
    );
    expect(element.querySelector('mat-expansion-panel-header')?.getAttribute('aria-expanded')).toBe(
      'false',
    );
    expect(
      Number(element.querySelector('mat-progress-bar')?.getAttribute('aria-valuenow')),
    ).toBeCloseTo(33.3, 1);
  });

  it('shows every todo after it is expanded', () => {
    const header = fixture.nativeElement.querySelector('mat-expansion-panel-header') as HTMLElement;
    header.click();
    fixture.detectChanges();

    const entries = Array.from(
      (fixture.nativeElement as HTMLElement).querySelectorAll('.entry-content'),
      (entry) => entry.textContent?.trim(),
    );
    expect(entries).toEqual(['Inspect the project', 'Update the implementation', 'Run tests']);
    expect(fixture.nativeElement.querySelector('.completed-entry')).not.toBeNull();
    expect(fixture.nativeElement.querySelector('.current-entry mat-icon')?.textContent.trim()).toBe(
      'progress_activity',
    );
    expect(fixture.nativeElement.querySelector('.entry-status')).toBeNull();
  });
});
