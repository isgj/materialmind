import { ComponentFixture, TestBed } from '@angular/core/testing';
import { By } from '@angular/platform-browser';
import { MatSelect, MatSelectChange } from '@angular/material/select';

import { MCPElicitationComponent } from './mcp-elicitation.component';
import { MCPElicitationState } from './mcp-elicitation.models';

describe('MCPElicitationComponent', () => {
  let fixture: ComponentFixture<MCPElicitationComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [MCPElicitationComponent] });
    fixture = TestBed.createComponent(MCPElicitationComponent);
  });

  it('validates and submits a form elicitation', () => {
    fixture.componentRef.setInput('request', formRequest());
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    const submit = fixture.nativeElement.querySelector(
      '.elicitation-actions button',
    ) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    const input = fixture.nativeElement.querySelector('input[matinput]') as HTMLInputElement;
    input.value = 'MaterialMind';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();

    expect(submit.disabled).toBe(false);
    submit.click();
    expect(submitted).toHaveBeenCalledWith({
      id: 'elicitation-1',
      action: 'accept',
      content: { project: 'MaterialMind' },
    });
  });

  it('presents URL elicitation as an external link and submits consent', () => {
    fixture.componentRef.setInput('request', {
      ...formRequest(),
      mode: 'url',
      url: 'https://example.com/authorize',
      requestedSchema: undefined,
    } satisfies MCPElicitationState);
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    const link = fixture.nativeElement.querySelector('.elicitation-actions a') as HTMLAnchorElement;
    expect(link.target).toBe('_blank');
    expect(link.rel).toContain('noopener');
    expect(link.href).toBe('https://example.com/authorize');
    link.dispatchEvent(new MouseEvent('click', { bubbles: true }));

    expect(submitted).toHaveBeenCalledWith({
      id: 'elicitation-1',
      action: 'accept',
      content: {},
    });
  });

  it('treats an unchecked required boolean as an explicit response', () => {
    fixture.componentRef.setInput('request', {
      ...formRequest(),
      requestedSchema: {
        type: 'object',
        required: ['confirmed'],
        properties: {
          confirmed: { type: 'boolean', title: 'Confirm' },
        },
      },
    } satisfies MCPElicitationState);
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    const submit = fixture.nativeElement.querySelector(
      '.elicitation-actions button',
    ) as HTMLButtonElement;
    expect(submit.disabled).toBe(false);
    submit.click();

    expect(submitted).toHaveBeenCalledWith({
      id: 'elicitation-1',
      action: 'accept',
      content: { confirmed: false },
    });
  });

  it('renders and submits ACP titled multi-select options', () => {
    fixture.componentRef.setInput('request', {
      ...formRequest(),
      requestedSchema: {
        type: 'object',
        required: ['targets'],
        properties: {
          targets: {
            type: 'array',
            title: 'Targets',
            minItems: 1,
            items: {
              anyOf: [
                { const: 'backend', title: 'Backend' },
                { const: 'frontend', title: 'Frontend' },
              ],
            },
          },
        },
      },
    } satisfies MCPElicitationState);
    const submitted = vi.fn();
    fixture.componentInstance.submitted.subscribe(submitted);
    fixture.detectChanges();

    const select = fixture.debugElement.query(By.directive(MatSelect))
      .componentInstance as MatSelect;
    expect(select.multiple).toBe(true);
    select.selectionChange.emit(new MatSelectChange(select, ['backend', 'frontend']));
    fixture.detectChanges();

    const submit = fixture.nativeElement.querySelector(
      '.elicitation-actions button',
    ) as HTMLButtonElement;
    expect(submit.disabled).toBe(false);
    submit.click();
    expect(submitted).toHaveBeenCalledWith({
      id: 'elicitation-1',
      action: 'accept',
      content: { targets: ['backend', 'frontend'] },
    });
  });
});

function formRequest(): MCPElicitationState {
  return {
    id: 'elicitation-1',
    sessionId: 'session-1',
    toolCallId: 'tool-1',
    serverId: 'server-1',
    serverName: 'Project server',
    mode: 'form',
    message: 'Choose a project',
    status: 'pending',
    requestedSchema: {
      type: 'object',
      required: ['project'],
      properties: {
        project: { type: 'string', title: 'Project' },
      },
    },
  };
}
