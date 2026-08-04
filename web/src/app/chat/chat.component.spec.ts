import { Clipboard } from '@angular/cdk/clipboard';
import { Signal, WritableSignal, signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MatDialog } from '@angular/material/dialog';
import { MatSnackBar } from '@angular/material/snack-bar';
import { ActivatedRoute, convertToParamMap } from '@angular/router';
import { of } from 'rxjs';

import { ApiService } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import {
  AgentRun,
  AppSession,
  LlmModel,
  LlmProvider,
  ReasoningEffort,
  SessionNotes,
  ToolApprovalResolution,
  TranscriptItem,
} from '../core/models';
import { REASONING_EFFORT_OPTIONS } from '../core/reasoning-effort';
import { ChatComponent } from './chat.component';
import { LiveActivity } from './chat-timeline';
import { SessionNotesDialog } from './session-notes-dialog/session-notes-dialog';

interface ComposerKeyboardHarness {
  attachStream(runId: string): void;
  activeRun: WritableSignal<AgentRun | null>;
  session: Signal<AppSession | null>;
  credentialWarning: Signal<string | null>;
  composerAttachments: Signal<readonly { id: string; name: string; size: number }[]>;
  composerMultiline: WritableSignal<boolean>;
  composerModel: WritableSignal<{ message: string }>;
  liveActivity: WritableSignal<LiveActivity[]>;
  transcript: WritableSignal<TranscriptItem[]>;
  olderTranscriptCursor: WritableSignal<number | null>;
  loadOlderTranscript(): Promise<void>;
  applyToolApprovalResolution(resolution: ToolApprovalResolution): void;
  handleComposerPaste(event: ClipboardEvent): void;
  handleComposerKeydown(event: KeyboardEvent): void;
  load(sessionId: string): Promise<void>;
  scheduleScrollToBottom(shouldScroll: boolean): void;
  scrollToBottom(): void;
  selectedReasoningEffort: WritableSignal<ReasoningEffort | 'model-default'>;
  sessionNotes: WritableSignal<SessionNotes | null>;
  send(): Promise<void>;
  supportsReasoningEffort: Signal<boolean>;
  reasoningEffortOptions: Signal<readonly { value: ReasoningEffort; label: string }[]>;
  updateComposerLayout(textarea: HTMLTextAreaElement): void;
  updateToolApproval(
    approvalId: string,
    changes: {
      status: 'pending' | 'submitting' | 'approved' | 'executing' | 'denied';
      reason?: string;
    },
  ): void;
}

class StubEventSource {
  static latest: StubEventSource | null = null;

  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private readonly listeners = new Map<string, Array<(event: MessageEvent<string>) => void>>();

  constructor(readonly url: string) {
    StubEventSource.latest = this;
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  close(): void {}

  emit(type: string, data: unknown): void {
    const event = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }
}

describe('ChatComponent composer keyboard', () => {
  const session: AppSession = {
    id: 'session-1',
    workspaceId: 'workspace-1',
    title: 'Original title',
    runtimeType: 'adk',
    selectedLlmModelId: 'model-1',
    acpAgentId: null,
    acpConfigOptions: [],
    status: 'idle',
    createdAt: '2026-07-21T08:00:00Z',
    updatedAt: '2026-07-21T08:00:00Z',
  };
  const openAIProvider: LlmProvider = {
    id: 'provider-1',
    name: 'OpenAI Responses',
    apiCompatibility: 'openai-responses',
    authType: 'bearer_env',
    bearerTokenEnvVar: 'OPENAI_TOKEN',
    credentialAvailable: true,
    createdAt: '2026-07-21T08:00:00Z',
    updatedAt: '2026-07-21T08:00:00Z',
  };
  const openAIModel: LlmModel = {
    id: 'model-1',
    llmProviderId: openAIProvider.id,
    name: 'GPT Test',
    modelId: 'gpt-test',
    contextWindowTokens: 128_000,
    maxOutputTokens: 8192,
    reasoningEffort: 'high',
    createdAt: '2026-07-21T08:00:00Z',
    updatedAt: '2026-07-21T08:00:00Z',
  };
  const anthropicProvider: LlmProvider = {
    ...openAIProvider,
    id: 'provider-anthropic',
    name: 'Anthropic Messages',
    apiCompatibility: 'anthropic',
  };
  const anthropicModel: LlmModel = {
    ...openAIModel,
    llmProviderId: anthropicProvider.id,
    name: 'Claude Test',
    modelId: 'claude-test',
  };

  let allSessions: WritableSignal<AppSession[]>;
  let llmModels: WritableSignal<LlmModel[]>;
  let llmProviders: WritableSignal<LlmProvider[]>;
  let stateLoading: WritableSignal<boolean>;
  let component: ComposerKeyboardHarness;
  let fixture: ComponentFixture<ChatComponent>;
  const clipboardCopy = vi.fn();
  const snackBarOpen = vi.fn();
  const startRun = vi.fn();
  const transcriptPage = vi.fn();
  const getSessionNotes = vi.fn();

  beforeEach(() => {
    const paramMap = convertToParamMap({ sessionId: session.id });
    allSessions = signal([{ ...session }]);
    llmModels = signal([]);
    llmProviders = signal([]);
    stateLoading = signal(false);
    startRun.mockReset();
    transcriptPage.mockReset();
    transcriptPage.mockResolvedValue({ items: [], hasMore: false });
    getSessionNotes.mockReset();
    getSessionNotes.mockResolvedValue({
      sessionId: session.id,
      content: '',
      revision: 0,
    } satisfies SessionNotes);
    clipboardCopy.mockReset();
    clipboardCopy.mockReturnValue(true);
    snackBarOpen.mockReset();
    TestBed.configureTestingModule({
      providers: [
        { provide: Clipboard, useValue: { copy: clipboardCopy } },
        {
          provide: AppState,
          useValue: {
            llmModels,
            llmProviders,
            workspaces: signal([]),
            allSessions,
            loading: stateLoading,
            selectedWorkspaceId: signal(null),
            selectWorkspace: vi.fn(),
            refreshSessions: vi.fn(),
            setSessionStatus: vi.fn(),
          },
        },
        {
          provide: ApiService,
          useValue: {
            getSession: vi.fn().mockResolvedValue({ ...session }),
            transcriptPage,
            getSessionNotes,
            listRuns: vi.fn().mockResolvedValue([]),
            startRun,
          },
        },
        {
          provide: ActivatedRoute,
          useValue: { paramMap: of(paramMap), snapshot: { paramMap } },
        },
        { provide: MatSnackBar, useValue: { open: snackBarOpen } },
      ],
    });

    fixture = TestBed.createComponent(ChatComponent);
    component = fixture.componentInstance as unknown as ComposerKeyboardHarness;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    StubEventSource.latest = null;
  });

  it('synchronizes the loaded session when shared state changes', async () => {
    await fixture.whenStable();

    allSessions.set([{ ...session, title: 'Renamed session' }]);

    await fixture.whenStable();

    expect(component.session()?.title).toBe('Renamed session');
  });

  it('does not show a model warning while shared state is loading', async () => {
    stateLoading.set(true);
    await fixture.whenStable();

    expect(component.credentialWarning()).toBeNull();

    stateLoading.set(false);

    expect(component.credentialWarning()).toBe('Add an LLM model to run the agent.');
  });

  it('does not show a model warning before chat history has loaded', async () => {
    expect(component.credentialWarning()).toBeNull();

    await fixture.whenStable();

    expect(component.credentialWarning()).toBe('Add an LLM model to run the agent.');
  });

  it('uses soft wrapping for long prompt text', async () => {
    await fixture.whenStable();

    const textarea = fixture.nativeElement.querySelector('.composer-input') as HTMLTextAreaElement;
    expect(textarea.getAttribute('wrap')).toBe('soft');
  });

  it('shows the configured reasoning effort for OpenAI-compatible models', async () => {
    llmModels.set([openAIModel]);
    llmProviders.set([openAIProvider]);
    await fixture.whenStable();
    fixture.detectChanges();

    expect(component.supportsReasoningEffort()).toBe(true);
    const controls = Array.from(
      fixture.nativeElement.querySelectorAll(
        '.composer-model-field, .composer-reasoning-field',
      ) as NodeListOf<HTMLElement>,
    );
    expect(controls[0]?.classList.contains('composer-model-field')).toBe(true);
    expect(controls[1]?.classList.contains('composer-reasoning-field')).toBe(true);
    expect(controls[0]?.querySelector('mat-icon')?.textContent?.trim()).toBe('bolt');
    const field = fixture.nativeElement.querySelector('.composer-reasoning-field') as HTMLElement;
    expect(field).not.toBeNull();
    expect(field.textContent).toContain('High');
    expect(field.querySelector('mat-icon')?.textContent?.trim()).toBe('psychology');
    expect(REASONING_EFFORT_OPTIONS.map((option) => option.value)).toEqual([
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
      'ultra',
    ]);
  });

  it('offers Anthropic-compatible effort without the OpenAI-only ultra level', async () => {
    llmModels.set([anthropicModel]);
    llmProviders.set([anthropicProvider]);
    await fixture.whenStable();
    fixture.detectChanges();

    expect(component.supportsReasoningEffort()).toBe(true);
    expect(component.reasoningEffortOptions().map((option) => option.value)).toEqual([
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ]);
    expect(fixture.nativeElement.querySelector('.composer-reasoning-field')).not.toBeNull();
  });

  it('shows session notes in a Markdown dialog only when notes exist', async () => {
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('[aria-label="View session notes"]')).toBeNull();

    const notes: SessionNotes = {
      sessionId: session.id,
      content: '# Decisions\n\nUse the workspace API.',
      revision: 2,
      updatedAt: '2026-08-03T12:00:00Z',
    };
    component.sessionNotes.set(notes);
    fixture.detectChanges();
    const open = vi.spyOn(TestBed.inject(MatDialog), 'open').mockReturnValue(undefined as never);

    const button = fixture.nativeElement.querySelector(
      '[aria-label="View session notes"]',
    ) as HTMLButtonElement;
    button.click();

    expect(open).toHaveBeenCalledWith(SessionNotesDialog, {
      data: notes,
      width: '720px',
      maxWidth: '96vw',
      maxHeight: '88vh',
    });
  });

  it('uses the default Material ligature class for the stop action icon', async () => {
    await fixture.whenStable();

    component.activeRun.set({
      id: 'run-1',
      sessionId: session.id,
      status: 'running',
      runtimeType: 'adk',
      llmProviderId: openAIProvider.id,
      llmProviderName: openAIProvider.name,
      llmModelId: openAIModel.id,
      llmModelName: openAIModel.name,
      apiCompatibility: openAIProvider.apiCompatibility,
      modelId: openAIModel.modelId,
      contextWindowTokens: openAIModel.contextWindowTokens,
      maxOutputTokens: openAIModel.maxOutputTokens,
      reasoningEffort: openAIModel.reasoningEffort,
      userMessage: 'Review the workspace',
      createdAt: '2026-07-21T08:00:00Z',
      updatedAt: '2026-07-21T08:00:00Z',
    });
    fixture.detectChanges();

    const stopIcon = fixture.nativeElement.querySelector('.stop-action mat-icon') as HTMLElement;

    expect(stopIcon.textContent?.trim()).toBe('stop');
    expect(stopIcon.classList.contains('material-icons')).toBe(true);
  });

  it('copies an assistant response as Markdown only from its explicit action', async () => {
    await fixture.whenStable();
    const markdown = '## Result\n\n- first\n- second';
    component.transcript.set([
      {
        id: 'message-1',
        kind: 'message',
        role: 'assistant',
        text: markdown,
        createdAt: '2026-07-21T08:01:00Z',
      },
    ]);
    fixture.detectChanges();

    const messageText = fixture.nativeElement.querySelector('.message-text') as HTMLElement;
    const copyEvent = new Event('copy', { bubbles: true, cancelable: true });
    messageText.dispatchEvent(copyEvent);

    expect(copyEvent.defaultPrevented).toBe(false);
    expect(clipboardCopy).not.toHaveBeenCalled();

    const copyButton = fixture.nativeElement.querySelector(
      'button[aria-label="Copy response as Markdown"]',
    ) as HTMLButtonElement;
    copyButton.click();

    expect(clipboardCopy).toHaveBeenCalledWith(markdown);
    expect(snackBarOpen).toHaveBeenCalledWith('Copied as Markdown', 'Dismiss', { duration: 2500 });
  });

  it('sends a prompt-level reasoning effort override', async () => {
    llmModels.set([openAIModel]);
    llmProviders.set([openAIProvider]);
    startRun.mockRejectedValue(new Error('stop after request'));
    await fixture.whenStable();

    component.composerModel.set({ message: 'Review the workspace' });
    component.selectedReasoningEffort.set('low');
    await component.send();

    expect(startRun).toHaveBeenCalledWith(session.id, {
      message: 'Review the workspace',
      llmModelId: openAIModel.id,
      reasoningEffort: 'low',
      attachments: [],
    });
  });

  it('queues a pasted file and allows it to be removed before sending', async () => {
    await fixture.whenStable();
    const file = new File(['review context'], 'context.txt', { type: 'text/plain' });
    const preventDefault = vi.fn();

    component.handleComposerPaste({
      clipboardData: {
        items: [{ kind: 'file', getAsFile: () => file }],
      },
      preventDefault,
    } as unknown as ClipboardEvent);

    await vi.waitFor(() => expect(component.composerAttachments()).toHaveLength(1));
    fixture.detectChanges();

    expect(preventDefault).toHaveBeenCalledOnce();
    expect(component.composerAttachments()[0]?.name).toBe('context.txt');
    const attachment = fixture.nativeElement.querySelector('.composer-attachments') as HTMLElement;
    expect(attachment.textContent).toContain('context.txt');

    const removeButton = attachment.querySelector(
      'button[aria-label="Remove context.txt"]',
    ) as HTMLButtonElement;
    removeButton.click();
    fixture.detectChanges();

    expect(component.composerAttachments()).toHaveLength(0);
  });

  it('does not let a late approval acknowledgement replace the executing state', async () => {
    await fixture.whenStable();
    component.liveActivity.set([
      {
        kind: 'tool',
        id: 'fetch-1',
        name: 'fetch_url',
        input: { url: 'https://example.com' },
        approval: {
          id: 'approval-1',
          kind: 'fetch_url',
          prompt: 'Allow this fetch request?',
          icon: 'language',
          targetLabel: 'URL',
          target: 'https://example.com',
          status: 'pending',
        },
      },
    ]);

    component.updateToolApproval('approval-1', { status: 'executing' });
    component.applyToolApprovalResolution({
      id: 'approval-1',
      toolCallId: 'fetch-1',
      approved: true,
    });

    const activity = component.liveActivity()[0];
    expect(activity.kind === 'tool' ? activity.approval?.status : null).toBe('executing');
  });

  it('keeps the composer width stable for visually wrapped text', () => {
    const textarea = document.createElement('textarea');
    textarea.value = 'A long prompt that wraps visually but contains no explicit newline.';

    component.updateComposerLayout(textarea);

    expect(component.composerMultiline()).toBe(false);

    textarea.value = 'An explicit\nnew line';
    component.updateComposerLayout(textarea);

    expect(component.composerMultiline()).toBe(true);
  });

  it('forces the transcript to the bottom after loading a session', async () => {
    await fixture.whenStable();
    const scheduleScroll = vi
      .spyOn(component, 'scheduleScrollToBottom')
      .mockImplementation(() => undefined);

    await component.load(session.id);

    expect(scheduleScroll).toHaveBeenCalledWith(true);
  });

  it('silently skips unavailable MCP servers when reconnecting to a run', async () => {
    await fixture.whenStable();
    vi.stubGlobal('EventSource', StubEventSource);

    component.attachStream('run-1');
    StubEventSource.latest?.emit('mcp_server_unavailable', {
      sessionId: session.id,
      serverId: 'server-1',
      serverName: 'Offline server',
      error: 'connection refused',
    });

    expect(snackBarOpen).not.toHaveBeenCalled();
  });

  it('prepends older transcript pages without duplicating items', async () => {
    await fixture.whenStable();
    component.transcript.set([
      {
        id: 'newer',
        kind: 'message',
        role: 'assistant',
        text: 'Newer',
        createdAt: '2026-07-21T08:01:00Z',
      },
    ]);
    component.olderTranscriptCursor.set(1);
    transcriptPage.mockResolvedValueOnce({
      items: [
        {
          id: 'older',
          kind: 'message',
          role: 'assistant',
          text: 'Older',
          createdAt: '2026-07-21T08:00:00Z',
        },
        {
          id: 'newer',
          kind: 'message',
          role: 'assistant',
          text: 'Newer',
          createdAt: '2026-07-21T08:01:00Z',
        },
      ],
      hasMore: false,
    });

    await component.loadOlderTranscript();

    expect(component.transcript().map((item) => item.id)).toEqual(['older', 'newer']);
    expect(component.olderTranscriptCursor()).toBeNull();
  });

  it('scrolls the transcript end anchor into view', async () => {
    await fixture.whenStable();
    const anchor = fixture.nativeElement.querySelector('.transcript-end') as HTMLElement;
    const scrollIntoView = vi.fn();
    anchor.scrollIntoView = scrollIntoView;

    component.scrollToBottom();

    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'end' });
  });

  it('sends on Enter', () => {
    const send = vi.spyOn(component, 'send').mockResolvedValue();
    const textarea = document.createElement('textarea');
    textarea.value = 'Review the workspace';
    textarea.addEventListener('keydown', (event) => component.handleComposerKeydown(event));

    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      bubbles: true,
      cancelable: true,
    });
    textarea.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(send).toHaveBeenCalledOnce();
    expect(textarea.value).toBe('Review the workspace');
  });

  it('inserts a newline at the caret on Ctrl+Enter', () => {
    const send = vi.spyOn(component, 'send').mockResolvedValue();
    const textarea = document.createElement('textarea');
    const inputListener = vi.fn();
    textarea.value = 'firstsecond';
    textarea.setSelectionRange(5, 5);
    textarea.addEventListener('input', inputListener);
    textarea.addEventListener('keydown', (event) => component.handleComposerKeydown(event));

    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });
    textarea.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(textarea.value).toBe('first\nsecond');
    expect(textarea.selectionStart).toBe(6);
    expect(inputListener).toHaveBeenCalledOnce();
    expect(send).not.toHaveBeenCalled();
  });

  it('inserts a newline at the caret on Shift+Enter', () => {
    const send = vi.spyOn(component, 'send').mockResolvedValue();
    const textarea = document.createElement('textarea');
    const inputListener = vi.fn();
    textarea.value = 'firstsecond';
    textarea.setSelectionRange(5, 5);
    textarea.addEventListener('input', inputListener);
    textarea.addEventListener('keydown', (event) => component.handleComposerKeydown(event));

    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    textarea.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(textarea.value).toBe('first\nsecond');
    expect(textarea.selectionStart).toBe(6);
    expect(inputListener).toHaveBeenCalledOnce();
    expect(send).not.toHaveBeenCalled();
  });
});
