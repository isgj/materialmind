import { ComponentFixture, TestBed } from '@angular/core/testing';

import { McpToolDetails } from './mcp-tool-details';

describe('McpToolDetails', () => {
  let component: McpToolDetails;
  let fixture: ComponentFixture<McpToolDetails>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [McpToolDetails],
    }).compileComponents();

    fixture = TestBed.createComponent(McpToolDetails);
    component = fixture.componentInstance;
    fixture.componentRef.setInput('details', {
      serverId: 'server-1',
      serverName: 'Artifacts',
      toolName: 'inspect',
      toolTitle: 'Inspect artifact',
      cancelable: true,
      cancelling: false,
      message: 'Reading artifact',
      progress: 1,
      total: 2,
      logs: [{ level: 'info', logger: 'reader', data: 'Started' }],
      content: [{ type: 'text', text: '**Ready**' }],
      structuredContent: { count: 1 },
      isError: false,
    });
    fixture.componentRef.setInput('arguments', { path: '/artifact.txt' });
    await fixture.whenStable();
  });

  it('should create', () => {
    expect(component).toBeTruthy();
  });

  it('renders MCP progress, logs, and content', () => {
    fixture.detectChanges();
    const text = fixture.nativeElement.textContent;
    expect(text).toContain('Artifacts');
    expect(text).toContain('Reading artifact');
    expect(text).toContain('Server logs');
    expect(text).toContain('Ready');
  });

  it('replaces running progress with a completed failure summary', () => {
    fixture.componentRef.setInput('details', {
      serverId: 'server-1',
      serverName: 'Artifacts',
      toolName: 'inspect',
      cancelable: false,
      cancelling: false,
      logs: [],
      content: [],
      isError: true,
      error: 'MCP tool call timed out after 2m0s',
    });
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('.mcp-progress')).toBeNull();
    expect(root.querySelector('.mcp-error-summary')?.textContent).toContain(
      'MCP tool call timed out after 2m0s',
    );
  });
});
