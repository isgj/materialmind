import { ComponentFixture, TestBed } from '@angular/core/testing';

import { AgentActivityComponent } from './agent-activity.component';
import { AgentActivityStep } from './chat-timeline';

describe('AgentActivityComponent', () => {
  let fixture: ComponentFixture<AgentActivityComponent>;

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [AgentActivityComponent] });
    fixture = TestBed.createComponent(AgentActivityComponent);
  });

  it('renders completed read-only tools as compact headers', () => {
    fixture.componentRef.setInput('steps', completedSteps());
    fixture.detectChanges();

    const headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('false');
    expect(headers[1].getAttribute('aria-expanded')).toBe('false');
    expect(fixture.nativeElement.querySelector('.step-details')).toBeNull();
    expect(fixture.nativeElement.textContent).not.toContain('hidden directory result');
    expect(fixture.nativeElement.textContent).not.toContain('hidden file result');
    const icons = Array.from<HTMLElement>(
      fixture.nativeElement.querySelectorAll('.activity-marker mat-icon'),
    ).map((icon) => icon.textContent?.trim());
    expect(icons).toEqual(['folder_open', 'description']);
    const stateIcons = Array.from<HTMLElement>(
      fixture.nativeElement.querySelectorAll('.step-state-icon'),
    ).map((icon) => icon.textContent?.trim());
    expect(stateIcons).toEqual(['check', 'check']);
    expect(fixture.nativeElement.textContent).not.toContain('Complete');
    expect(fixture.nativeElement.querySelector('.mat-expansion-indicator')).toBeNull();
  });

  it('renders completed context compaction as a compact status row', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'context-compaction:invocation-1:8',
        toolName: 'context_compaction',
        label: 'Context compacted',
        icon: 'compress',
        status: 'complete',
        input: { estimatedTokensBefore: 115_000 },
        output: { state: 'completed', estimatedTokensAfter: 36_000 },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    const header = activityHeaders(fixture)[0];
    expect(header.getAttribute('aria-expanded')).toBe('false');
    expect(header.getAttribute('aria-disabled')).toBe('true');
    expect(header.textContent).toContain('Context compacted');
    expect(fixture.nativeElement.querySelector('.step-details')).toBeNull();
  });

  it('keeps note reads compact and renders note updates as Markdown', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'read-notes',
        toolName: 'read_session_notes',
        label: 'Read session notes',
        icon: 'note_alt',
        status: 'complete',
        output: { state: 'read', revision: 1, content: 'hidden notes' },
        sessionNotes: {
          operation: 'read',
          state: 'read',
          revision: 1,
          bytes: 12,
          content: 'hidden notes',
        },
      },
      {
        id: 'update-notes',
        toolName: 'update_session_notes',
        label: 'Updated session notes',
        icon: 'edit_note',
        status: 'complete',
        input: { content: '# Decisions\n\n- Preserve the API.', expectedRevision: 1 },
        output: { state: 'updated', revision: 2, bytes: 32 },
        sessionNotes: {
          operation: 'update',
          state: 'updated',
          revision: 2,
          expectedRevision: 1,
          bytes: 32,
          content: '# Decisions\n\n- Preserve the API.',
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    const headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-disabled')).toBe('true');
    expect(fixture.nativeElement.textContent).not.toContain('hidden notes');
    headers[1].click();
    fixture.detectChanges();

    const content = selectedContent(fixture);
    expect(content).toContain('Revision 2');
    expect(content).toContain('32 bytes');
    expect(content).toContain('Decisions');
    expect(content).toContain('Preserve the API.');
    expect(fixture.nativeElement.querySelector('app-session-notes-details h1')).not.toBeNull();
    expect(content).not.toContain('expectedRevision');
  });

  it('summarizes a completed fetch without rendering its response body', () => {
    fixture.componentRef.setInput('steps', [
      ...completedSteps(),
      {
        id: 'fetch',
        toolName: 'fetch_url',
        label: 'Fetch example.com',
        icon: 'language',
        status: 'complete',
        input: { url: 'https://example.com/docs' },
        output: {
          state: 'fetched',
          url: 'https://example.com/docs',
          httpStatus: 200,
          contentType: 'text/html; charset=utf-8',
          content: '<html>raw response body</html>',
        },
        fetch: {
          state: 'fetched',
          requestedUrl: 'https://example.com/docs',
          httpStatus: 200,
          contentType: 'text/html; charset=utf-8',
          truncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    const headers = activityHeaders(fixture);
    headers[2].click();
    fixture.detectChanges();

    expect(headers[2].getAttribute('aria-expanded')).toBe('true');
    expect(selectedContent(fixture)).toContain('HTTP 200');
    expect(selectedContent(fixture)).toContain('text/html; charset=utf-8');
    expect(fixture.nativeElement.textContent).not.toContain('raw response body');
  });

  it('opens a running command step but leaves other running tools closed', () => {
    fixture.componentRef.setInput('steps', [
      ...completedSteps(),
      {
        id: 'search',
        toolName: 'grep',
        label: 'Search source',
        icon: 'search',
        status: 'running',
        input: { path: 'src' },
      },
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run tests',
        icon: 'terminal',
        status: 'running',
        input: { command: 'go', args: ['test', './...'] },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const headers = activityHeaders(fixture);
    expect(headers[2].getAttribute('aria-expanded')).toBe('false');
    expect(headers[3].getAttribute('aria-expanded')).toBe('true');
    expect(selectedContent(fixture)).toContain('go');
  });

  it('closes a command once it completes and opens a newly created command', () => {
    const firstStep: AgentActivityStep = {
      id: 'first',
      toolName: 'run_command',
      label: 'Run first command',
      icon: 'terminal',
      status: 'running',
      input: { command: 'first' },
    };
    const secondStep: AgentActivityStep = {
      id: 'second',
      toolName: 'run_command',
      label: 'Run second command',
      icon: 'terminal',
      status: 'running',
      input: { command: 'second' },
    };
    fixture.componentRef.setInput('steps', [firstStep]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    fixture.componentRef.setInput('steps', [
      { ...firstStep, status: 'complete', output: { state: 'fetched' } },
      secondStep,
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    const headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('false');
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');

    headers[0].click();
    fixture.detectChanges();
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');

    fixture.detectChanges();
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
  });

  it('uses distinct indicators for a running step and an approval wait', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'running',
        label: 'Run tests',
        icon: 'terminal',
        status: 'running',
        input: { command: 'go' },
      },
      {
        id: 'approval',
        label: 'Fetch documentation',
        icon: 'language',
        status: 'approval_required',
        input: { url: 'https://example.com' },
        approval: {
          id: 'approval-fetch',
          kind: 'fetch_url',
          prompt: 'Allow this fetch request?',
          icon: 'language',
          targetLabel: 'URL',
          target: 'https://example.com',
          status: 'pending',
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('.step-state .step-running-indicator')).not.toBeNull();
    expect(root.querySelector('.step-state .step-approval-indicator')?.textContent?.trim()).toBe(
      'pending_actions',
    );
    expect(root.querySelector('.step-title .step-running-indicator')).toBeNull();
    expect(root.textContent).not.toContain('Approval needed');
  });

  it('offers call-level cancellation and renders MCP progress', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'mcp-call',
        toolName: 'mcp_artifacts_inspect',
        label: 'Run Inspect artifact',
        icon: 'extension',
        status: 'running',
        input: { path: '/artifact.txt' },
        mcp: {
          serverId: 'server-1',
          serverName: 'Artifacts',
          toolName: 'inspect',
          toolTitle: 'Inspect artifact',
          cancelable: true,
          cancelling: false,
          message: 'Reading artifact',
          progress: 1,
          total: 2,
          logs: [],
          content: [],
          isError: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    const cancellation = vi.fn();
    fixture.componentInstance.mcpCancellation.subscribe(cancellation);
    fixture.detectChanges();

    const stop = fixture.nativeElement.querySelector('.mcp-stop-action') as HTMLButtonElement;
    expect(stop).not.toBeNull();
    expect(stop.querySelector('mat-icon')?.textContent?.trim()).toBe('stop');
    expect(fixture.nativeElement.querySelector('.step-running-indicator')).toBeNull();

    activityHeaders(fixture)[0].click();
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelectorAll('.mcp-progress mat-progress-bar')).toHaveLength(
      1,
    );
    expect(fixture.nativeElement.textContent).toContain('Reading artifact');

    stop.click();

    expect(cancellation).toHaveBeenCalledWith('mcp-call');
  });

  it('opens a clarification step and shows its form with a waiting indicator', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'ask',
        toolName: 'ask_user',
        label: 'Ask: Which package should be changed?',
        icon: 'question_answer',
        status: 'input_required',
        input: {},
        userInput: {
          id: 'input-1',
          toolCallId: 'ask',
          toolName: 'ask_user',
          status: 'pending',
          questions: [
            {
              id: 'scope',
              question: 'Which package should be changed?',
              options: [{ id: 'api', label: 'API package' }],
            },
          ],
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(activityHeaders(fixture)[0]?.getAttribute('aria-expanded')).toBe('true');
    expect(root.querySelector('app-user-input')).not.toBeNull();
    expect(root.querySelector('.step-input-indicator')?.textContent?.trim()).toBe(
      'contact_support',
    );
    expect(root.textContent).not.toContain('Response needed');
  });

  it('uses distinct right-side icons for denied, cancelled, failed, and incomplete steps', () => {
    fixture.componentRef.setInput('steps', [
      { id: 'denied', label: 'Denied', icon: 'language', status: 'denied' },
      { id: 'cancelled', label: 'Cancelled', icon: 'terminal', status: 'cancelled' },
      { id: 'failed', label: 'Failed', icon: 'terminal', status: 'failed' },
      { id: 'incomplete', label: 'Incomplete', icon: 'search', status: 'incomplete' },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    const icons = Array.from<HTMLElement>(
      fixture.nativeElement.querySelectorAll('.step-state-icon'),
    ).map((icon) => icon.textContent?.trim());
    expect(icons).toEqual(['block', 'cancel', 'error_outline', 'help_outline']);
    expect(fixture.nativeElement.querySelectorAll('.step-state-problem')).toHaveLength(3);
  });

  it('shows queued and executing approval states while every step remains selectable', () => {
    const firstApproval: AgentActivityStep = {
      id: 'edit-main',
      label: 'Edit main.go',
      icon: 'edit',
      status: 'approval_required',
      input: { path: 'main.go' },
      approval: {
        id: 'approval-main',
        kind: 'file_edit',
        prompt: 'Allow this file edit?',
        icon: 'edit_note',
        targetLabel: 'File',
        target: 'main.go',
        status: 'pending',
      },
    };
    const secondApproval: AgentActivityStep = {
      id: 'edit-proxy',
      label: 'Edit proxy.conf.json',
      icon: 'edit',
      status: 'approval_required',
      input: { path: 'proxy.conf.json' },
      approval: {
        id: 'approval-proxy',
        kind: 'file_edit',
        prompt: 'Allow this file edit?',
        icon: 'edit_note',
        targetLabel: 'File',
        target: 'proxy.conf.json',
        status: 'pending',
      },
    };
    fixture.componentRef.setInput('steps', [firstApproval, secondApproval]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    let headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');
    expect(headers[1].getAttribute('aria-disabled')).not.toBe('true');

    fixture.componentRef.setInput('steps', [
      {
        ...firstApproval,
        status: 'queued',
        approval: { ...firstApproval.approval!, status: 'approved' },
      },
      secondApproval,
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');
    expect(headers[0].querySelector('.step-state-queued')?.textContent?.trim()).toBe('schedule');

    fixture.componentRef.setInput('steps', [
      {
        ...firstApproval,
        status: 'running',
        approval: { ...firstApproval.approval!, status: 'executing' },
      },
      secondApproval,
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
    expect(headers[0].querySelector('.step-running-indicator')).not.toBeNull();

    fixture.componentRef.setInput('steps', [
      {
        ...firstApproval,
        status: 'complete',
        output: { state: 'fetched' },
        approval: { ...firstApproval.approval!, status: 'approved' },
      },
      secondApproval,
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('false');
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');
    headers[0].click();
    fixture.detectChanges();
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
  });

  it('keeps each approval panel state independent through queued and running transitions', () => {
    const firstApproval = commandApprovalStep('first', 'First command');
    const secondApproval = commandApprovalStep('second', 'Second command');
    fixture.componentRef.setInput('steps', [firstApproval, secondApproval]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    fixture.componentRef.setInput('steps', [
      {
        ...firstApproval,
        status: 'running',
        approval: { ...firstApproval.approval!, status: 'executing' },
      },
      {
        ...secondApproval,
        status: 'queued',
        approval: { ...secondApproval.approval!, status: 'approved' },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    let headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('true');
    expect(headers[1].getAttribute('aria-expanded')).toBe('true');

    headers[1].click();
    fixture.detectChanges();
    expect(headers[1].getAttribute('aria-expanded')).toBe('false');

    fixture.componentRef.setInput('steps', [
      {
        ...firstApproval,
        status: 'complete',
        approval: { ...firstApproval.approval!, status: 'approved' },
      },
      {
        ...secondApproval,
        status: 'running',
        approval: { ...secondApproval.approval!, status: 'executing' },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    headers = activityHeaders(fixture);
    expect(headers[0].getAttribute('aria-expanded')).toBe('false');
    expect(headers[1].getAttribute('aria-expanded')).toBe('false');
  });

  it('renders every file in a batch approval', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'batch-edit',
        label: 'Change 2 files',
        icon: 'edit',
        status: 'approval_required',
        input: { changes: [] },
        approval: {
          id: 'approval-batch',
          kind: 'file_edit',
          prompt: 'Allow these file changes?',
          icon: 'difference',
          targetLabel: 'Files',
          target: '2 files',
          files: [
            {
              operation: 'create',
              path: 'created.txt',
              diff: '--- /dev/null\n+++ b/created.txt\n@@ -0,0 +1 @@\n+created\n',
            },
            {
              operation: 'delete',
              path: 'obsolete.txt',
              diff: '--- a/obsolete.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-obsolete\n',
            },
          ],
          status: 'pending',
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const previews = fixture.nativeElement.querySelectorAll('app-diff-preview');
    expect(previews).toHaveLength(2);
    expect(fixture.nativeElement.textContent).toContain('created.txt');
    expect(fixture.nativeElement.textContent).toContain('Created');
    expect(fixture.nativeElement.textContent).toContain('obsolete.txt');
    expect(fixture.nativeElement.textContent).toContain('Deleted');
  });

  it('emits a refusal with the optional reason', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'fetch',
        label: 'Fetch example.com',
        icon: 'language',
        status: 'approval_required',
        input: { url: 'https://example.com/docs' },
        approval: {
          id: 'approval-1',
          kind: 'fetch_url',
          prompt: 'Allow this fetch request?',
          icon: 'language',
          targetLabel: 'URL',
          target: 'https://example.com/docs',
          status: 'pending',
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    const decision = vi.fn();
    fixture.componentInstance.approvalDecision.subscribe(decision);
    fixture.detectChanges();

    const reason = fixture.nativeElement.querySelector('textarea') as HTMLTextAreaElement;
    reason.value = 'Use the checked-in documentation.';
    reason.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    const buttons = fixture.nativeElement.querySelectorAll('.approval-actions button');
    buttons[1].click();

    expect(decision).toHaveBeenCalledWith({
      id: 'approval-1',
      approved: false,
      reason: 'Use the checked-in documentation.',
    });
  });

  it('renders a complete command in a Bash block and keeps live output as plain text', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        label: 'Run go test ./...',
        icon: 'terminal',
        status: 'running',
        input: { command: 'go', args: ['test', './...'] },
        approval: {
          id: 'approval-command',
          kind: 'command_run',
          prompt: 'Allow this command to run?',
          icon: 'terminal',
          targetLabel: 'Command',
          target: 'go test ./...',
          command: {
            commandLine: 'go test ./...',
            workingDirectory: '/home/user/repository',
            timeoutSeconds: 120,
          },
          status: 'approved',
        },
        command: {
          commandLine: 'go test ./...',
          workingDirectory: '/home/user/repository',
          timeoutSeconds: 120,
          stdout: '<script>not markup</script>\n',
          stderr: 'one failure\n',
          timedOut: false,
          stdoutTruncated: false,
          stderrTruncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    const metadataIcons = Array.from<HTMLElement>(
      root.querySelectorAll('.command-context > .command-meta-item mat-icon'),
    ).map((icon) => icon.textContent?.trim());
    const commandBlock = root.querySelector('.command-line pre code.language-bash');
    expect(metadataIcons).toEqual(['folder_open', 'timer']);
    expect(commandBlock?.textContent).toBe('go test ./...');
    expect(commandBlock?.parentElement?.tabIndex).toBe(0);
    expect(root.querySelector('.approval-target .target-label')).toBeNull();
    expect(root.textContent).toContain('/home/user/repository');
    expect(root.textContent).toContain('<script>not markup</script>');
    expect(root.querySelector('script')).toBeNull();
    expect(root.textContent).toContain('one failure');
  });

  it('does not repeat the running status for an executing approved command', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run go test ./...',
        icon: 'terminal',
        status: 'running',
        input: { command: 'go', args: ['test', './...'] },
        approval: {
          id: 'approval-command',
          kind: 'command_run',
          prompt: 'Allow this command to run?',
          icon: 'terminal',
          targetLabel: 'Command',
          target: 'go test ./...',
          command: {
            commandLine: 'go test ./...',
            workingDirectory: '/home/user/repository',
            timeoutSeconds: 120,
          },
          status: 'executing',
        },
        command: {
          commandLine: 'go test ./...',
          workingDirectory: '/home/user/repository',
          timeoutSeconds: 120,
          stdout: '',
          stderr: '',
          timedOut: false,
          stdoutTruncated: false,
          stderrTruncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.textContent).toContain('Action running');
    expect(root.textContent).toContain('Waiting for command output');
    expect(root.querySelector('.command-summary')).toBeNull();
  });

  it('summarizes a failed command without repeating the command as an error', () => {
    const commandLine = 'node -e "process.exit(1)"';
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: commandLine,
        icon: 'terminal',
        status: 'failed',
        command: {
          commandLine,
          workingDirectory: '/home/user/repository',
          timeoutSeconds: 120,
          stdout: '',
          stderr: '',
          state: 'failed',
          timedOut: false,
          stdoutTruncated: false,
          stderrTruncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    activityHeaders(fixture)[0].click();
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector('.command-summary')?.textContent).toContain('Run exited with error');
    expect(root.querySelector('.command-error')).toBeNull();
    expect(root.textContent?.match(/node -e/g)).toHaveLength(2);
  });

  it('uses a stored exit code for a failed command summary', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run tests',
        icon: 'terminal',
        status: 'failed',
        command: {
          commandLine: 'go test ./...',
          workingDirectory: '/home/user/repository',
          timeoutSeconds: 120,
          stdout: '',
          stderr: '',
          state: 'failed',
          exitCode: 2,
          timedOut: false,
          stdoutTruncated: false,
          stderrTruncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    activityHeaders(fixture)[0].click();
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('.command-summary')?.textContent).toContain(
      'Exited with code 2',
    );
  });

  it('collapses completed command output by default', () => {
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run go test ./...',
        icon: 'terminal',
        status: 'complete',
        input: { command: 'go', args: ['test', './...'] },
        command: {
          commandLine: 'go test ./...',
          workingDirectory: '/home/user/repository',
          timeoutSeconds: 120,
          stdout: 'ok\n',
          stderr: '',
          exitCode: 0,
          timedOut: false,
          stdoutTruncated: false,
          stderrTruncated: false,
        },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    activityHeaders(fixture)[0].click();
    fixture.detectChanges();

    const outputHeader = fixture.nativeElement.querySelector(
      '.command-output-panel .mat-expansion-panel-header',
    ) as HTMLElement;
    expect(outputHeader.getAttribute('aria-expanded')).toBe('false');

    outputHeader.click();
    fixture.detectChanges();

    expect(outputHeader.getAttribute('aria-expanded')).toBe('true');
    expect(fixture.nativeElement.textContent).toContain('ok');
  });

  it('keeps streaming command output scrolled to the bottom', () => {
    const command = {
      commandLine: 'go test ./...',
      workingDirectory: '/home/user/repository',
      timeoutSeconds: 120,
      stdout: 'first line\n',
      stderr: '',
      timedOut: false,
      stdoutTruncated: false,
      stderrTruncated: false,
    };
    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run go test ./...',
        icon: 'terminal',
        status: 'running',
        command,
      },
    ] satisfies AgentActivityStep[]);
    fixture.componentRef.setInput('running', true);
    fixture.detectChanges();

    const output = fixture.nativeElement.querySelector(
      '[data-command-step-id="command"]',
    ) as HTMLPreElement;
    Object.defineProperty(output, 'scrollHeight', { configurable: true, value: 320 });
    output.scrollTop = 24;

    fixture.componentRef.setInput('steps', [
      {
        id: 'command',
        toolName: 'run_command',
        label: 'Run go test ./...',
        icon: 'terminal',
        status: 'running',
        command: { ...command, stdout: 'first line\nsecond line\n' },
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    expect(output.scrollTop).toBe(320);
  });

  it('renders completed ACP edits as diffs without raw input or result JSON', () => {
    const diff = '--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n';
    fixture.componentRef.setInput('steps', [
      {
        id: 'acp-edit',
        toolName: 'edit_file',
        label: 'Update main.go',
        icon: 'edit',
        status: 'complete',
        input: { title: 'raw input marker' },
        output: { state: 'completed', title: 'raw result marker' },
        fileChanges: [{ operation: 'update', path: 'main.go', diff }],
      },
    ] satisfies AgentActivityStep[]);
    fixture.detectChanges();

    activityHeaders(fixture)[0].click();
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelectorAll('app-diff-preview')).toHaveLength(1);
    expect(root.textContent).toContain('main.go');
    expect(root.textContent).toContain('old');
    expect(root.textContent).toContain('new');
    expect(root.textContent).not.toContain('raw input marker');
    expect(root.textContent).not.toContain('raw result marker');
  });
});

function selectedContent(fixture: ComponentFixture<AgentActivityComponent>): string {
  const headers = activityHeaders(fixture);
  const selectedHeader = headers.find((header) => header.getAttribute('aria-expanded') === 'true');
  const contentId = selectedHeader?.getAttribute('aria-controls');
  return contentId ? (fixture.nativeElement.querySelector(`#${contentId}`)?.textContent ?? '') : '';
}

function activityHeaders(fixture: ComponentFixture<AgentActivityComponent>): HTMLElement[] {
  return Array.from<HTMLElement>(
    fixture.nativeElement.querySelectorAll('.activity-panel > .mat-expansion-panel-header'),
  );
}

function completedSteps(): AgentActivityStep[] {
  return [
    {
      id: 'list',
      toolName: 'list_directory',
      label: 'Inspect .',
      icon: 'folder_open',
      status: 'complete',
      input: { path: '.' },
      output: { entries: ['hidden directory result'] },
    },
    {
      id: 'read',
      toolName: 'read_file',
      label: 'Read go.mod',
      icon: 'description',
      status: 'complete',
      input: { path: 'go.mod' },
      output: { content: 'hidden file result' },
    },
  ];
}

function commandApprovalStep(id: string, label: string): AgentActivityStep {
  return {
    id,
    toolName: 'run_command',
    label,
    icon: 'terminal',
    status: 'approval_required',
    approval: {
      id: `approval-${id}`,
      kind: 'command_run',
      prompt: 'Allow this command to run?',
      icon: 'terminal',
      targetLabel: 'Command',
      target: label,
      status: 'pending',
    },
  };
}
