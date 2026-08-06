import { DatePipe } from '@angular/common';
import { Clipboard } from '@angular/cdk/clipboard';
import { CdkTextareaAutosize } from '@angular/cdk/text-field';
import {
  Component,
  ElementRef,
  OnDestroy,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormField, form, maxLength, required } from '@angular/forms/signals';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatMenuModule } from '@angular/material/menu';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSelectChange, MatSelectModule } from '@angular/material/select';
import { MatSlideToggleChange, MatSlideToggleModule } from '@angular/material/slide-toggle';
import { MatSnackBar } from '@angular/material/snack-bar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { ActivatedRoute } from '@angular/router';
import { firstValueFrom, map } from 'rxjs';

import { ApiService, errorMessage } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import {
  AcpConfigOption,
  AcpConfigSelectOption,
  AcpConfigSelectValue,
  AgentPlan,
  AgentRun,
  AppSession,
  LlmModel,
  MCPElicitationResolution,
  StreamACPElicitationCompletion,
  ReasoningEffort,
  SessionNotes,
  StreamCommandOutput,
  StreamACPUsage,
  StreamContextCompaction,
  StreamMCPCallFinished,
  StreamMCPCallStarted,
  StreamMCPLog,
  StreamMCPProgress,
  StreamMCPElicitationRequest,
  StreamMCPToolsChanged,
  StreamMessage,
  StreamSubagentCompleted,
  StreamSubagentStarted,
  StreamThought,
  StreamToolApproval,
  StreamToolApprovalStarted,
  StreamToolCall,
  StreamToolResult,
  StreamToolStatus,
  StreamUserInputRequest,
  ToolApprovalResolution,
  TranscriptItem,
  UserInputResolution,
} from '../core/models';
import {
  formatReasoningEffort,
  reasoningEffortOptionsForCompatibility,
} from '../core/reasoning-effort';
import { MarkdownPipe } from '../shared/markdown.pipe';
import {
  AttachmentPreview,
  MessageAttachmentsComponent,
} from '../shared/message-attachments.component';
import { ToolApprovalDecision } from '../shared/tool-approval/tool-approval.models';
import { MCPElicitationSubmission } from '../shared/mcp-elicitation/mcp-elicitation.models';
import { UserInputSubmission } from '../shared/user-input/user-input.models';
import {
  AcpSessionOptionChange,
  AcpSessionOptionsDialogComponent,
  AcpSessionOptionsDialogData,
} from './acp-session-options-dialog.component';
import {
  McpContextDialogComponent,
  McpContextDialogData,
  McpContextDialogResult,
} from './mcp-context-dialog.component';
import { SessionNotesDialog } from './session-notes-dialog/session-notes-dialog';
import { AgentActivityComponent } from './agent-activity.component';
import { AgentDelegationComponent } from './agent-delegation';
import { AgentNoteComponent } from './agent-note.component';
import { AgentPlanComponent } from './agent-plan.component';
import {
  LiveActivity,
  LiveToolActivity,
  applyLiveMCPCallFinished,
  applyLiveMCPCallStarted,
  applyLiveMCPElicitationRequest,
  applyLiveMCPElicitationResolution,
  applyLiveACPElicitationCompletion,
  applyLiveMCPLog,
  applyLiveMCPProgress,
  appendLiveCommandOutput,
  applyLiveContextCompaction,
  buildChatTimeline,
  buildLiveChatTimeline,
  buildLiveToolApproval,
  buildLiveUserInput,
  latestAgentPlan,
  transcriptBeforeActiveInvocation,
  updateLiveToolActivity,
} from './chat-timeline';

const modelDefaultReasoning = 'model-default' as const;
type ComposerReasoningEffort = ReasoningEffort | typeof modelDefaultReasoning;

interface ComposerAttachment extends AttachmentPreview {
  file: File;
}

const maxAttachmentCount = 10;
const maxAttachmentSize = 10 * 1024 * 1024;
const maxAttachmentsSize = 25 * 1024 * 1024;

@Component({
  selector: 'app-chat',
  imports: [
    CdkTextareaAutosize,
    DatePipe,
    FormField,
    MarkdownPipe,
    AgentActivityComponent,
    AgentDelegationComponent,
    AgentNoteComponent,
    AgentPlanComponent,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatMenuModule,
    MatProgressBarModule,
    MatProgressSpinnerModule,
    MatSelectModule,
    MatSlideToggleModule,
    MatTooltipModule,
    MessageAttachmentsComponent,
  ],
  templateUrl: './chat.component.html',
  styleUrl: './chat.component.scss',
})
export class ChatComponent implements OnDestroy {
  protected readonly state = inject(AppState);
  private readonly api = inject(ApiService);
  private readonly clipboard = inject(Clipboard);
  private readonly route = inject(ActivatedRoute);
  private readonly snackBar = inject(MatSnackBar);
  private readonly dialog = inject(MatDialog);
  private readonly scrollRegion = viewChild<ElementRef<HTMLElement>>('scrollRegion');
  private readonly transcriptEnd = viewChild<ElementRef<HTMLElement>>('transcriptEnd');

  protected readonly sessionId = toSignal(
    this.route.paramMap.pipe(map((params) => params.get('sessionId') ?? '')),
    { initialValue: '' },
  );
  protected readonly session = signal<AppSession | null>(null);
  protected readonly sessionNotes = signal<SessionNotes | null>(null);
  protected readonly transcript = signal<TranscriptItem[]>([]);
  protected readonly olderTranscriptCursor = signal<number | null>(null);
  protected readonly loadingOlderTranscript = signal(false);
  protected readonly runs = signal<AgentRun[]>([]);
  protected readonly selectedModelId = signal('');
  protected readonly selectedReasoningEffort =
    signal<ComposerReasoningEffort>(modelDefaultReasoning);
  protected readonly composerAttachments = signal<ComposerAttachment[]>([]);
  protected readonly pendingMessageAttachments = signal<AttachmentPreview[]>([]);
  protected readonly activeRun = signal<AgentRun | null>(null);
  protected readonly timeline = computed(() =>
    buildChatTimeline(
      transcriptBeforeActiveInvocation(
        this.transcript(),
        this.activeRun()?.invocationId || undefined,
      ),
      this.runs(),
    ),
  );
  protected readonly pendingUserText = signal('');
  protected readonly streamedText = signal('');
  protected readonly liveActivity = signal<LiveActivity[]>([]);
  protected readonly liveTimeline = computed(() => buildLiveChatTimeline(this.liveActivity()));
  protected readonly livePlan = signal<AgentPlan | null>(null);
  protected readonly acpUsage = signal<StreamACPUsage | null>(null);
  protected readonly visiblePlan = computed(() => {
    const activeRun = this.activeRun();
    if (activeRun) {
      const livePlan = this.livePlan();
      if (livePlan) {
        return livePlan;
      }
      return activeRun.invocationId
        ? latestAgentPlan(this.transcript(), activeRun.invocationId)
        : null;
    }
    if (this.pendingUserText()) {
      return this.livePlan();
    }
    const latestInvocationId = this.runs().at(-1)?.invocationId;
    return this.livePlan() ?? latestAgentPlan(this.transcript(), latestInvocationId ?? undefined);
  });
  protected readonly loading = signal(true);
  protected readonly streamConnected = signal(false);
  private readonly composerModel = signal({ message: '' });
  protected readonly composerForm = form(this.composerModel, (path) => {
    required(path.message);
    maxLength(path.message, 32000);
  });
  protected readonly workspace = computed(() => {
    const session = this.session();
    return this.state.workspaces().find((item) => item.id === session?.workspaceId) ?? null;
  });
  protected readonly selectedModel = computed(
    () => this.state.llmModels().find((item) => item.id === this.selectedModelId()) ?? null,
  );
  protected readonly isAcpSession = computed(() => this.session()?.runtimeType === 'acp');
  protected readonly acpAgent = computed(() => {
    const agentId = this.session()?.acpAgentId;
    return this.state.acpAgents().find((agent) => agent.id === agentId) ?? null;
  });
  protected readonly acpConfigOptions = computed(() => this.session()?.acpConfigOptions ?? []);
  protected readonly acpPrimaryConfigOptions = computed(() => {
    const options = this.acpConfigOptions();
    return (['model', 'thought_level'] as const).flatMap((category) =>
      options.filter((option) => option.category === category),
    );
  });
  protected readonly acpSecondaryConfigOptions = computed(() =>
    this.acpConfigOptions().filter(
      (option) => option.category !== 'model' && option.category !== 'thought_level',
    ),
  );
  protected readonly selectedProvider = computed(() => {
    const model = this.selectedModel();
    return this.state.llmProviders().find((item) => item.id === model?.llmProviderId) ?? null;
  });
  protected readonly reasoningEffortOptions = computed(() =>
    reasoningEffortOptionsForCompatibility(this.selectedProvider()?.apiCompatibility),
  );
  protected readonly supportsReasoningEffort = computed(
    () => this.reasoningEffortOptions().length > 0,
  );
  protected readonly reasoningEffortDefaultLabel = computed(() => {
    const configured = this.selectedModel()?.reasoningEffort;
    return configured ? `Model default (${formatReasoningEffort(configured)})` : 'Model default';
  });
  protected readonly reasoningEffortTriggerLabel = computed(() => {
    const selected = this.selectedReasoningEffort();
    const effective =
      selected === modelDefaultReasoning ? this.selectedModel()?.reasoningEffort : selected;
    return effective ? formatReasoningEffort(effective) : 'Default';
  });
  protected readonly reasoningEffortTooltip = computed(() => {
    const selected = this.selectedReasoningEffort();
    return selected === modelDefaultReasoning
      ? `Reasoning effort: ${this.reasoningEffortDefaultLabel()}`
      : `Reasoning effort: ${formatReasoningEffort(selected)}`;
  });
  protected readonly credentialWarning = computed(() => {
    if (this.loading() || this.state.loading()) {
      return null;
    }
    if (this.isAcpSession()) {
      const agent = this.acpAgent();
      if (!agent) {
        return 'The selected ACP agent is unavailable.';
      }
      return agent.available ? null : `Command ${agent.command} is not available.`;
    }
    const model = this.selectedModel();
    if (!model) {
      return this.state.llmModels().length === 0
        ? 'Add an LLM model to run the agent.'
        : 'Select an LLM model to run the agent.';
    }
    const provider = this.selectedProvider();
    if (!provider) {
      return 'The selected model provider is unavailable.';
    }
    if (!provider.credentialAvailable) {
      return provider.authType === 'bearer_env'
        ? `Environment variable ${provider.bearerTokenEnvVar} is not set.`
        : 'No credential is stored for the selected provider.';
    }
    return null;
  });
  protected readonly runtimeReady = computed(() =>
    this.isAcpSession()
      ? (this.acpAgent()?.available ?? false)
      : (this.selectedProvider()?.credentialAvailable ?? false),
  );
  protected readonly canSend = computed(
    () =>
      !this.activeRun() &&
      (this.composerModel().message.trim().length > 0 || this.composerAttachments().length > 0) &&
      this.runtimeReady(),
  );
  protected readonly hasComposerContent = computed(
    () => this.composerModel().message.trim().length > 0 || this.composerAttachments().length > 0,
  );
  protected readonly hasSessionNotes = computed(
    () => (this.sessionNotes()?.content.trim().length ?? 0) > 0,
  );
  protected readonly showComposerToolbar = computed(
    () =>
      !this.isAcpSession() ||
      this.acpPrimaryConfigOptions().length > 0 ||
      this.acpSecondaryConfigOptions().length > 0 ||
      this.acpUsage() !== null ||
      this.hasSessionNotes(),
  );
  protected readonly composerMultiline = signal(false);

  private eventSource: EventSource | null = null;
  private loadGeneration = 0;
  private chatScrollFrame: number | null = null;
  private attachmentSequence = 0;
  private preservingTranscriptScroll = false;

  constructor() {
    effect(() => {
      const id = this.sessionId();
      untracked(() => {
        if (id) {
          void this.load(id);
        }
      });
    });
    effect(() => {
      const stateSession = this.state
        .allSessions()
        .find((session) => session.id === this.sessionId());
      if (!stateSession) {
        return;
      }
      untracked(() => {
        this.session.set(stateSession);
        this.selectedModelId.set(
          stateSession.runtimeType === 'adk'
            ? (stateSession.selectedLlmModelId ?? this.state.llmModels()[0]?.id ?? '')
            : '',
        );
      });
    });
    effect(() => {
      this.transcript();
      this.streamedText();
      this.liveActivity();
      this.livePlan();
      if (!this.preservingTranscriptScroll) {
        this.scheduleScrollToBottom(this.isNearChatBottom());
      }
    });
  }

  ngOnDestroy(): void {
    this.closeStream();
    this.releaseAttachmentPreviews([
      ...this.composerAttachments(),
      ...this.pendingMessageAttachments(),
    ]);
    if (this.chatScrollFrame !== null) {
      window.cancelAnimationFrame(this.chatScrollFrame);
    }
  }

  protected async send(): Promise<void> {
    const session = this.session();
    const message = this.composerModel().message.trim();
    const attachments = this.composerAttachments();
    const llmModelId = this.selectedModelId();
    if (
      !session ||
      (!message && attachments.length === 0) ||
      (session.runtimeType === 'adk' && !llmModelId) ||
      this.activeRun() ||
      !this.runtimeReady()
    ) {
      return;
    }
    try {
      this.pendingUserText.set(message);
      this.pendingMessageAttachments.set(attachments);
      this.streamedText.set('');
      this.liveActivity.set([]);
      this.livePlan.set(null);
      this.acpUsage.set(null);
      this.composerModel.set({ message: '' });
      this.composerAttachments.set([]);
      this.composerMultiline.set(false);
      const reasoningSelection = this.selectedReasoningEffort();
      const reasoningEffort =
        this.supportsReasoningEffort() && reasoningSelection !== modelDefaultReasoning
          ? reasoningSelection
          : null;
      const run = await this.api.startRun(session.id, {
        message,
        llmModelId: session.runtimeType === 'adk' ? llmModelId : null,
        reasoningEffort,
        attachments: attachments.map((attachment) => attachment.file),
      });
      this.pendingMessageAttachments.set(run.attachments ?? []);
      this.releaseAttachmentPreviews(attachments);
      this.activeRun.set(run);
      this.runs.update((items) => [...items, run]);
      this.attachStream(run.id);
      await this.state.refreshSessions();
    } catch (error) {
      this.pendingUserText.set('');
      this.pendingMessageAttachments.set([]);
      this.composerModel.set({ message });
      this.composerAttachments.set(attachments);
      this.showError(error);
    }
  }

  protected attachSelectedFiles(input: HTMLInputElement): void {
    const files = Array.from(input.files ?? []);
    input.value = '';
    void this.addComposerFiles(files);
  }

  protected openSessionNotes(): void {
    const notes = this.sessionNotes();
    if (!notes?.content.trim()) {
      return;
    }
    this.dialog.open<SessionNotesDialog, SessionNotes>(SessionNotesDialog, {
      data: notes,
      width: '720px',
      maxWidth: '96vw',
      maxHeight: '88vh',
    });
  }

  protected async openMcpContext(): Promise<void> {
    const sessionId = this.sessionId();
    if (!sessionId || this.activeRun()) {
      return;
    }
    const result = await firstValueFrom(
      this.dialog
        .open<McpContextDialogComponent, McpContextDialogData, McpContextDialogResult>(
          McpContextDialogComponent,
          {
            data: { sessionId },
            width: '760px',
            maxWidth: '96vw',
          },
        )
        .afterClosed(),
    );
    if (!result) {
      return;
    }
    try {
      if (result.kind === 'resource') {
        await this.addComposerFiles(resourceFiles(result.value));
        return;
      }
      const context = promptComposerContext(result.value);
      if (context.text) {
        const current = this.composerModel().message;
        this.composerModel.set({
          message: [current.trimEnd(), context.text].filter(Boolean).join('\n\n'),
        });
        this.composerMultiline.set(this.composerModel().message.includes('\n'));
      }
      await this.addComposerFiles(context.files);
    } catch (error) {
      this.showError(error);
    }
  }

  protected handleComposerPaste(event: ClipboardEvent): void {
    const files = Array.from(event.clipboardData?.items ?? []).flatMap((item) => {
      if (item.kind !== 'file') {
        return [];
      }
      const file = item.getAsFile();
      return file ? [file] : [];
    });
    if (files.length === 0) {
      return;
    }
    event.preventDefault();
    void this.addComposerFiles(files);
  }

  protected removeComposerAttachment(id: string): void {
    const attachment = this.composerAttachments().find((item) => item.id === id);
    if (attachment) {
      this.releaseAttachmentPreviews([attachment]);
    }
    this.composerAttachments.update((items) => items.filter((item) => item.id !== id));
  }

  private async addComposerFiles(files: readonly File[]): Promise<void> {
    const current = [...this.composerAttachments()];
    let totalSize = current.reduce((total, attachment) => total + attachment.size, 0);
    for (const file of files) {
      if (
        current.some(
          (attachment) =>
            attachment.file.name === file.name &&
            attachment.file.size === file.size &&
            attachment.file.lastModified === file.lastModified,
        )
      ) {
        continue;
      }
      if (current.length >= maxAttachmentCount) {
        this.showAttachmentError('A message can contain at most 10 attachments.');
        break;
      }
      if (file.size > maxAttachmentSize) {
        this.showAttachmentError(`${file.name} exceeds the 10 MiB limit.`);
        continue;
      }
      if (totalSize + file.size > maxAttachmentsSize) {
        this.showAttachmentError('Attachments exceed the 25 MiB total limit.');
        break;
      }
      let mimeType: string;
      try {
        mimeType = await supportedFileType(file);
      } catch {
        this.showAttachmentError(`${file.name} must be UTF-8 text, PDF, PNG, JPEG, GIF, or WebP.`);
        continue;
      }
      const previewUrl = mimeType.startsWith('image/') ? URL.createObjectURL(file) : undefined;
      current.push({
        id: `composer-attachment-${++this.attachmentSequence}`,
        name: file.name,
        mimeType,
        size: file.size,
        file,
        previewUrl,
      });
      totalSize += file.size;
    }
    this.composerAttachments.set(current);
  }

  private showAttachmentError(message: string): void {
    this.snackBar.open(message, 'Dismiss', { duration: 6000 });
  }

  protected copyMarkdown(markdown: string): void {
    if (this.clipboard.copy(markdown)) {
      this.snackBar.open('Copied as Markdown', 'Dismiss', { duration: 2500 });
      return;
    }
    this.snackBar.open('Could not copy the response', 'Dismiss', { duration: 5000 });
  }

  private releaseAttachmentPreviews(attachments: readonly AttachmentPreview[]): void {
    for (const attachment of attachments) {
      if (attachment.previewUrl?.startsWith('blob:')) {
        URL.revokeObjectURL(attachment.previewUrl);
      }
    }
  }

  protected handleComposerKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' || event.isComposing) {
      return;
    }
    event.preventDefault();
    if (event.ctrlKey || event.metaKey || event.shiftKey) {
      const textarea = event.currentTarget as HTMLTextAreaElement;
      textarea.setRangeText('\n', textarea.selectionStart, textarea.selectionEnd, 'end');
      textarea.dispatchEvent(new Event('input', { bubbles: true }));
      return;
    }
    void this.send();
  }

  protected updateComposerLayout(textarea: HTMLTextAreaElement): void {
    this.composerMultiline.set(textarea.value.includes('\n'));
  }

  protected async cancel(): Promise<void> {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    try {
      await this.api.cancelRun(run.id);
    } catch (error) {
      this.showError(error);
    }
  }

  protected async cancelMCPToolCall(toolCallId: string): Promise<void> {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    this.updateMCPActivity(toolCallId, { cancelling: true });
    try {
      await this.api.cancelMCPToolCall(run.id, toolCallId);
    } catch (error) {
      this.updateMCPActivity(toolCallId, { cancelling: false });
      this.showError(error);
    }
  }

  protected async resolveToolApproval(decision: ToolApprovalDecision): Promise<void> {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    this.updateToolApproval(decision.id, { status: 'submitting' });
    this.syncSessionWaitingStatus();
    try {
      const resolution = await this.api.resolveToolApproval(run.id, decision.id, {
        approved: decision.approved,
        reason: decision.reason,
        optionId: decision.optionId,
      });
      this.applyToolApprovalResolution(resolution);
    } catch (error) {
      this.updateToolApproval(decision.id, { status: 'pending' });
      this.syncSessionWaitingStatus();
      this.showError(error);
    }
  }

  protected async resolveUserInput(submission: UserInputSubmission): Promise<void> {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    this.updateUserInput(submission.id, { status: 'submitting' });
    this.syncSessionWaitingStatus();
    try {
      const resolution = await this.api.resolveUserInput(run.id, submission.id, submission.answers);
      this.applyUserInputResolution(resolution);
    } catch (error) {
      this.updateUserInput(submission.id, { status: 'pending' });
      this.syncSessionWaitingStatus();
      this.showError(error);
    }
  }

  protected async resolveMCPElicitation(submission: MCPElicitationSubmission): Promise<void> {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    this.updateMCPElicitation(submission.id, { status: 'submitting' });
    this.syncSessionWaitingStatus();
    try {
      const resolution = await this.api.resolveMCPElicitation(
        run.id,
        submission.id,
        submission.action,
        submission.content,
      );
      this.applyMCPElicitationResolution(resolution);
    } catch (error) {
      this.updateMCPElicitation(submission.id, { status: 'pending' });
      this.syncSessionWaitingStatus();
      this.showError(error);
    }
  }

  protected async selectModel(change: MatSelectChange): Promise<void> {
    const modelId = String(change.value);
    const session = this.session();
    if (!session || session.runtimeType !== 'adk' || !modelId) {
      return;
    }
    const previousReasoningEffort = this.selectedReasoningEffort();
    this.selectedModelId.set(modelId);
    this.selectedReasoningEffort.set(modelDefaultReasoning);
    try {
      const updated = await this.api.updateSession(session.id, {
        title: session.title,
        llmModelId: modelId,
      });
      this.session.set(updated);
      await this.state.refreshSessions();
    } catch (error) {
      this.selectedModelId.set(session.selectedLlmModelId ?? '');
      this.selectedReasoningEffort.set(previousReasoningEffort);
      this.showError(error);
    }
  }

  protected selectReasoningEffort(change: MatSelectChange): void {
    this.selectedReasoningEffort.set(change.value as ComposerReasoningEffort);
  }

  protected async setAcpConfigOption(
    option: AcpConfigOption,
    value: string | boolean,
  ): Promise<void> {
    const session = this.session();
    if (!session || session.runtimeType !== 'acp' || this.activeRun()) {
      return;
    }
    try {
      const updated = await this.api.setAcpSessionConfigOption(session.id, option.id, value);
      this.session.set(updated);
      await this.state.refreshSessions();
    } catch (error) {
      this.showError(error);
    }
  }

  protected selectAcpConfigOption(option: AcpConfigOption, change: MatSelectChange): void {
    void this.setAcpConfigOption(option, String(change.value));
  }

  protected toggleAcpConfigOption(option: AcpConfigOption, change: MatSlideToggleChange): void {
    void this.setAcpConfigOption(option, change.checked);
  }

  protected acpSelectValues(option: AcpConfigSelectOption): AcpConfigSelectValue[] {
    return option.options.flatMap((item) => ('options' in item ? item.options : [item]));
  }

  protected acpConfigValueLabel(option: AcpConfigSelectOption): string {
    return (
      this.acpSelectValues(option).find((item) => item.value === option.currentValue)?.name ??
      option.currentValue
    );
  }

  protected async openAcpSessionOptions(): Promise<void> {
    const session = this.session();
    const options = this.acpSecondaryConfigOptions();
    if (!session || session.runtimeType !== 'acp' || this.activeRun() || options.length === 0) {
      return;
    }
    const changes = await firstValueFrom(
      this.dialog
        .open<
          AcpSessionOptionsDialogComponent,
          AcpSessionOptionsDialogData,
          AcpSessionOptionChange[]
        >(AcpSessionOptionsDialogComponent, {
          data: { options },
          width: '560px',
        })
        .afterClosed(),
    );
    if (!changes || changes.length === 0) {
      return;
    }
    try {
      let updated = session;
      for (const change of changes) {
        updated = await this.api.setAcpSessionConfigOption(session.id, change.id, change.value);
      }
      this.session.set(updated);
      await this.state.refreshSessions();
    } catch (error) {
      this.showError(error);
    }
  }

  protected modelsForProvider(providerId: string): LlmModel[] {
    return this.state.llmModels().filter((model) => model.llmProviderId === providerId);
  }

  protected async loadOlderTranscript(): Promise<void> {
    const sessionId = this.sessionId();
    const cursor = this.olderTranscriptCursor();
    if (!sessionId || cursor === null || this.loadingOlderTranscript()) {
      return;
    }
    this.loadingOlderTranscript.set(true);
    const region = this.scrollRegion()?.nativeElement;
    const previousHeight = region?.scrollHeight ?? 0;
    const previousTop = region?.scrollTop ?? 0;
    try {
      const page = await this.api.transcriptPage(sessionId, cursor);
      if (sessionId !== this.sessionId()) {
        return;
      }
      const currentIDs = new Set(this.transcript().map((item) => item.id));
      const olderItems = (page.items ?? []).filter((item) => !currentIDs.has(item.id));
      this.olderTranscriptCursor.set(page.hasMore ? (page.nextCursor ?? null) : null);
      if (olderItems.length === 0) {
        return;
      }
      this.preservingTranscriptScroll = true;
      this.transcript.update((items) => [...olderItems, ...items]);
      if (region) {
        window.requestAnimationFrame(() => {
          region.scrollTop = previousTop + region.scrollHeight - previousHeight;
          this.preservingTranscriptScroll = false;
        });
      } else {
        this.preservingTranscriptScroll = false;
      }
    } catch (error) {
      this.showError(error);
    } finally {
      this.loadingOlderTranscript.set(false);
    }
  }

  private async load(sessionId: string): Promise<void> {
    const generation = ++this.loadGeneration;
    this.closeStream();
    this.releaseAttachmentPreviews([
      ...this.composerAttachments(),
      ...this.pendingMessageAttachments(),
    ]);
    this.composerAttachments.set([]);
    this.pendingMessageAttachments.set([]);
    this.loading.set(true);
    this.transcript.set([]);
    this.olderTranscriptCursor.set(null);
    this.activeRun.set(null);
    this.pendingUserText.set('');
    this.streamedText.set('');
    this.liveActivity.set([]);
    this.livePlan.set(null);
    this.acpUsage.set(null);
    this.sessionNotes.set(null);
    this.selectedReasoningEffort.set(modelDefaultReasoning);
    try {
      const [session, transcriptPage, runs, notes] = await Promise.all([
        this.api.getSession(sessionId),
        this.api.transcriptPage(sessionId),
        this.api.listRuns(sessionId),
        this.readSessionNotes(sessionId),
      ]);
      if (generation !== this.loadGeneration) {
        return;
      }
      const transcriptItems = transcriptPage.items ?? [];
      const runItems = runs ?? [];
      this.session.set(session);
      this.transcript.set(transcriptItems);
      this.olderTranscriptCursor.set(
        transcriptPage.hasMore ? (transcriptPage.nextCursor ?? null) : null,
      );
      this.runs.set(runItems);
      this.sessionNotes.set(notes);
      this.selectedModelId.set(
        session.runtimeType === 'adk'
          ? (session.selectedLlmModelId ?? this.state.llmModels()[0]?.id ?? '')
          : '',
      );
      if (
        this.state.workspaces().length > 0 &&
        this.state.selectedWorkspaceId() !== session.workspaceId
      ) {
        await this.state.selectWorkspace(session.workspaceId);
      }
      const running = [...runItems]
        .reverse()
        .find((item) => ['queued', 'running'].includes(item.status));
      if (running) {
        this.activeRun.set(running);
        this.pendingUserText.set(running.userMessage);
        this.pendingMessageAttachments.set(running.attachments ?? []);
        this.attachStream(running.id);
      }
    } catch (error) {
      if (generation === this.loadGeneration) {
        this.showError(error);
      }
    } finally {
      if (generation === this.loadGeneration) {
        this.loading.set(false);
        this.scheduleScrollToBottom(true);
      }
    }
  }

  private async readSessionNotes(sessionId: string): Promise<SessionNotes | null> {
    try {
      return await this.api.getSessionNotes(sessionId);
    } catch {
      return null;
    }
  }

  private async refreshSessionNotes(sessionId: string): Promise<void> {
    const notes = await this.readSessionNotes(sessionId);
    if (notes && sessionId === this.sessionId()) {
      this.sessionNotes.set(notes);
    }
  }

  private attachStream(runId: string): void {
    this.closeStream();
    const source = new EventSource(`/api/runs/${runId}/events`);
    this.eventSource = source;
    source.onopen = () => this.streamConnected.set(true);
    source.onerror = () => this.streamConnected.set(false);
    source.addEventListener('stream_reset', () => {
      this.closeStream();
      const id = this.sessionId();
      if (id) {
        void this.load(id);
      }
    });
    source.addEventListener('run', (event) => {
      const run = this.parseEvent<AgentRun>(event);
      if (run && (run.status === 'queued' || run.status === 'running')) {
        this.activeRun.set(run);
        this.state.setSessionStatus(run.sessionId, run.status);
      }
    });
    source.addEventListener('message_delta', (event) => {
      const message = this.parseEvent<StreamMessage>(event);
      if (message) {
        if (message.delegationId) {
          this.updateSubagentMessage(message, false);
        } else {
          this.streamedText.update((text) => text + message.text);
        }
      }
    });
    source.addEventListener('message_complete', (event) => {
      const message = this.parseEvent<StreamMessage>(event);
      if (message) {
        if (message.delegationId) {
          this.updateSubagentMessage(message, true);
        } else {
          this.streamedText.set(message.text);
        }
      }
    });
    source.addEventListener('thought_delta', (event) => {
      const thought = this.parseEvent<StreamThought>(event);
      if (thought) {
        this.updateLiveThought(thought, false);
      }
    });
    source.addEventListener('thought_replace', (event) => {
      const thought = this.parseEvent<StreamThought>(event);
      if (thought) {
        this.updateLiveThought(thought, true);
      }
    });
    source.addEventListener('plan_update', (event) => {
      const plan = this.parseEvent<AgentPlan>(event);
      if (plan && Array.isArray(plan.entries)) {
        this.livePlan.set(plan.entries.length > 0 ? plan : null);
      }
    });
    source.addEventListener('subagent_started', (event) => {
      const delegation = this.parseEvent<StreamSubagentStarted>(event);
      if (delegation) {
        this.startSubagent(delegation);
      }
    });
    source.addEventListener('subagent_completed', (event) => {
      const delegation = this.parseEvent<StreamSubagentCompleted>(event);
      if (delegation) {
        this.completeSubagent(delegation);
      }
    });
    source.addEventListener('tool_call', (event) => {
      const call = this.parseEvent<StreamToolCall>(event);
      if (!call) {
        return;
      }
      this.applyToolCall(call);
    });
    source.addEventListener('tool_status', (event) => {
      const status = this.parseEvent<StreamToolStatus>(event);
      if (!status) {
        return;
      }
      this.liveActivity.update(
        (items) =>
          updateLiveToolActivity(items, status.id, (item) => ({
            ...item,
            toolStatus: status.status,
          })).items,
      );
    });
    source.addEventListener('command_output', (event) => {
      const output = this.parseEvent<StreamCommandOutput>(event);
      if (
        !output ||
        (output.stream !== 'stdout' && output.stream !== 'stderr') ||
        typeof output.text !== 'string'
      ) {
        return;
      }
      this.liveActivity.update((items) => appendLiveCommandOutput(items, output));
    });
    source.addEventListener('context_compaction', (event) => {
      const update = this.parseEvent<StreamContextCompaction>(event);
      if (update?.id) {
        this.liveActivity.update((items) => applyLiveContextCompaction(items, update));
      }
    });
    source.addEventListener('acp_usage', (event) => {
      const usage = this.parseEvent<StreamACPUsage>(event);
      if (usage && Number.isFinite(usage.used) && Number.isFinite(usage.size)) {
        this.acpUsage.set(usage);
      }
    });
    source.addEventListener('mcp_call_started', (event) => {
      const call = this.parseEvent<StreamMCPCallStarted>(event);
      if (call?.toolCallId) {
        this.liveActivity.update((items) => applyLiveMCPCallStarted(items, call));
      }
    });
    source.addEventListener('mcp_call_finished', (event) => {
      const call = this.parseEvent<StreamMCPCallFinished>(event);
      if (call?.toolCallId && call.output) {
        this.liveActivity.update((items) => applyLiveMCPCallFinished(items, call));
      }
    });
    source.addEventListener('mcp_progress', (event) => {
      const progress = this.parseEvent<StreamMCPProgress>(event);
      if (progress?.toolCallId) {
        this.liveActivity.update((items) => applyLiveMCPProgress(items, progress));
      }
    });
    source.addEventListener('mcp_elicitation_request', (event) => {
      const request = this.parseEvent<StreamMCPElicitationRequest>(event);
      if (!request?.id || !request.toolCallId) {
        return;
      }
      this.liveActivity.update((items) => applyLiveMCPElicitationRequest(items, request));
      this.syncSessionWaitingStatus();
    });
    source.addEventListener('mcp_elicitation_resolved', (event) => {
      const resolution = this.parseEvent<MCPElicitationResolution>(event);
      if (resolution?.id) {
        this.applyMCPElicitationResolution(resolution);
      }
    });
    source.addEventListener('acp_elicitation_complete', (event) => {
      const completion = this.parseEvent<StreamACPElicitationCompletion>(event);
      if (completion?.id) {
        this.liveActivity.update((items) => applyLiveACPElicitationCompletion(items, completion));
      }
    });
    source.addEventListener('mcp_log', (event) => {
      const log = this.parseEvent<StreamMCPLog>(event);
      if (!log) {
        return;
      }
      let matched = false;
      this.liveActivity.update((items) => {
        const update = applyLiveMCPLog(items, log);
        matched = update.matched;
        return update.items;
      });
      if (!matched && mcpLogNeedsAttention(log.level)) {
        this.snackBar.open(`${log.serverName}: ${mcpLogText(log.data)}`, 'Dismiss', {
          duration: 8000,
        });
      }
    });
    source.addEventListener('mcp_tools_changed', (event) => {
      const update = this.parseEvent<StreamMCPToolsChanged>(event);
      if (!update) {
        return;
      }
      if (update.error) {
        this.snackBar.open(
          `Could not refresh tools from ${update.serverName}: ${update.error}`,
          'Dismiss',
          { duration: 8000 },
        );
        return;
      }
      const added = update.added?.length ?? 0;
      const removed = update.removed?.length ?? 0;
      const summary = added + removed > 0 ? ` (${added} added, ${removed} removed)` : '';
      this.snackBar.open(
        `Tools from ${update.serverName} changed${summary}. Updates apply on the next message.`,
        'Dismiss',
        { duration: 6000 },
      );
    });
    source.addEventListener('tool_result', (event) => {
      const result = this.parseEvent<StreamToolResult>(event);
      if (!result) {
        return;
      }
      this.liveActivity.update(
        (items) =>
          updateLiveToolActivity(items, result.id, (item) => ({
            ...item,
            output: result.output,
            approval:
              item.approval?.status === 'executing'
                ? { ...item.approval, status: 'approved' }
                : item.approval,
          })).items,
      );
      if (result.name.trim().toLowerCase() === 'update_session_notes') {
        void this.refreshSessionNotes(this.sessionId());
      }
    });
    source.addEventListener('tool_approval', (event) => {
      const request = this.parseEvent<StreamToolApproval>(event);
      if (!request) {
        return;
      }
      const approval = buildLiveToolApproval(request);
      this.liveActivity.update((items) => {
        const update = updateLiveToolActivity(items, request.toolCallId, (item) => ({
          ...item,
          approval,
        }));
        if (update.matched) {
          return update.items;
        }
        return [
          ...update.items,
          {
            kind: 'tool' as const,
            id: request.toolCallId,
            name: request.toolName,
            input: request.input,
            approval,
          },
        ];
      });
      this.syncSessionWaitingStatus();
    });
    source.addEventListener('tool_approval_resolved', (event) => {
      const resolution = this.parseEvent<ToolApprovalResolution>(event);
      if (resolution) {
        this.applyToolApprovalResolution(resolution);
      }
    });
    source.addEventListener('tool_approval_started', (event) => {
      const started = this.parseEvent<StreamToolApprovalStarted>(event);
      if (started) {
        this.updateToolApproval(started.id, { status: 'executing' });
        this.syncSessionWaitingStatus();
      }
    });
    source.addEventListener('user_input_request', (event) => {
      const request = this.parseEvent<StreamUserInputRequest>(event);
      if (!request) {
        return;
      }
      const userInput = buildLiveUserInput(request);
      this.liveActivity.update((items) => {
        const update = updateLiveToolActivity(items, request.toolCallId, (item) => ({
          ...item,
          userInput,
        }));
        if (update.matched) {
          return update.items;
        }
        return [
          ...update.items,
          {
            kind: 'tool' as const,
            id: request.toolCallId,
            name: request.toolName,
            input: { questions: request.questions },
            userInput,
          },
        ];
      });
      this.syncSessionWaitingStatus();
    });
    source.addEventListener('user_input_resolved', (event) => {
      const resolution = this.parseEvent<UserInputResolution>(event);
      if (resolution) {
        this.applyUserInputResolution(resolution);
      }
    });
    source.addEventListener('run_error', (event) => {
      const data = this.parseEvent<{ message: string }>(event);
      this.failRunningSubagents();
      if (data?.message) {
        this.snackBar.open(data.message, 'Dismiss', { duration: 8000 });
      }
    });
    source.addEventListener('done', () => {
      this.state.setSessionStatus(this.sessionId(), 'idle');
      this.closeStream();
      const id = this.sessionId();
      if (id) {
        void this.reloadAfterRun(id);
      }
    });
  }

  private async reloadAfterRun(sessionId: string): Promise<void> {
    try {
      const [session, transcriptPage, runs, notes] = await Promise.all([
        this.api.getSession(sessionId),
        this.api.transcriptPage(sessionId),
        this.api.listRuns(sessionId),
        this.readSessionNotes(sessionId),
      ]);
      if (sessionId !== this.sessionId()) {
        return;
      }
      this.session.set(session);
      this.transcript.set(transcriptPage.items ?? []);
      this.olderTranscriptCursor.set(
        transcriptPage.hasMore ? (transcriptPage.nextCursor ?? null) : null,
      );
      this.runs.set(runs ?? []);
      this.sessionNotes.set(notes);
      this.activeRun.set(null);
      this.pendingUserText.set('');
      this.pendingMessageAttachments.set([]);
      this.streamedText.set('');
      this.liveActivity.set([]);
      this.livePlan.set(null);
      this.scheduleScrollToBottom(true);
      await this.state.refreshSessions();
    } catch (error) {
      this.showError(error);
    }
  }

  private parseEvent<T>(event: Event): T | null {
    if (!(event instanceof MessageEvent) || typeof event.data !== 'string') {
      return null;
    }
    try {
      return JSON.parse(event.data) as T;
    } catch {
      return null;
    }
  }

  private applyToolApprovalResolution(resolution: ToolApprovalResolution): void {
    this.updateToolApproval(resolution.id, {
      status: resolution.approved ? 'approved' : 'denied',
      reason: resolution.reason,
    });
    this.syncSessionWaitingStatus();
  }

  private applyUserInputResolution(resolution: UserInputResolution): void {
    this.updateUserInput(resolution.id, {
      status: 'answered',
      answers: resolution.answers,
    });
    this.syncSessionWaitingStatus();
  }

  private applyMCPElicitationResolution(resolution: MCPElicitationResolution): void {
    this.liveActivity.update((items) => applyLiveMCPElicitationResolution(items, resolution));
    this.syncSessionWaitingStatus();
  }

  private updateLiveThought(thought: StreamThought, replace: boolean): void {
    let matched = false;
    this.liveActivity.update((items) => {
      const updated = items.map((item) => {
        if (item.kind !== 'note' || item.id !== thought.id) {
          return item;
        }
        matched = true;
        return { ...item, text: replace ? thought.text : item.text + thought.text };
      });
      return matched
        ? updated
        : [...updated, { kind: 'note' as const, id: thought.id, text: thought.text }];
    });
  }

  private startSubagent(delegation: StreamSubagentStarted): void {
    const note = this.streamedText().trim();
    let found = false;
    this.liveActivity.update((items) => {
      const updated = items.map((item): LiveActivity => {
        if (item.kind !== 'subagent' || item.id !== delegation.id) {
          return item;
        }
        found = true;
        return {
          ...item,
          name: delegation.name,
          label: delegation.label,
          task: delegation.task,
          status: 'running',
        };
      });
      if (found) {
        return updated;
      }
      const additions: LiveActivity[] = [];
      if (note) {
        additions.push({ kind: 'note', id: `note:${delegation.id}`, text: note });
      }
      additions.push({
        kind: 'subagent',
        id: delegation.id,
        name: delegation.name,
        label: delegation.label,
        task: delegation.task,
        status: 'running',
        activities: [],
      });
      return [...updated, ...additions];
    });
    if (note && !found) {
      this.streamedText.set('');
    }
  }

  private completeSubagent(delegation: StreamSubagentCompleted): void {
    this.liveActivity.update((items) =>
      items.map((item): LiveActivity => {
        if (item.kind !== 'subagent' || item.id !== delegation.id) {
          return item;
        }
        return {
          ...item,
          name: delegation.name,
          label: delegation.label,
          status:
            typeof delegation.output['error'] === 'string' &&
            delegation.output['error'].trim().length > 0
              ? 'failed'
              : 'complete',
          output: delegation.output,
        };
      }),
    );
  }

  private applyToolCall(call: StreamToolCall): void {
    const existing = updateLiveToolActivity(this.liveActivity(), call.id, (item) => ({
      ...item,
      name: call.name,
      input: call.input,
    }));
    if (existing.matched) {
      this.liveActivity.set(existing.items);
      return;
    }

    const tool: LiveToolActivity = {
      kind: 'tool',
      id: call.id,
      name: call.name,
      input: call.input,
    };
    if (call.delegationId) {
      let matched = false;
      this.liveActivity.update((items) =>
        items.map((item): LiveActivity => {
          if (item.kind !== 'subagent' || item.id !== call.delegationId) {
            return item;
          }
          matched = true;
          const activeNoteId = `subagent-note:${item.id}:active`;
          const activities = item.activities.map((activity): LiveActivity =>
            activity.kind === 'note' && activity.id === activeNoteId
              ? { ...activity, id: `subagent-note:${item.id}:before:${call.id}` }
              : activity,
          );
          return { ...item, activities: [...activities, tool] };
        }),
      );
      if (!matched) {
        this.liveActivity.update((items) => [
          ...items,
          {
            kind: 'subagent',
            id: call.delegationId as string,
            name: call.agentName || 'subagent',
            label: call.agentLabel || formatAgentName(call.agentName),
            task: '',
            status: 'running',
            activities: [tool],
          },
        ]);
      }
      return;
    }

    const note = this.streamedText().trim();
    const additions: LiveActivity[] = [];
    if (note) {
      additions.push({ kind: 'note', id: `note:${call.id}`, text: note });
    }
    additions.push(tool);
    this.liveActivity.update((items) => [...items, ...additions]);
    if (note) {
      this.streamedText.set('');
    }
  }

  private updateSubagentMessage(message: StreamMessage, replace: boolean): void {
    const delegationId = message.delegationId;
    if (!delegationId) {
      return;
    }
    const noteId = `subagent-note:${delegationId}:active`;
    let matched = false;
    this.liveActivity.update((items) =>
      items.map((item): LiveActivity => {
        if (item.kind !== 'subagent' || item.id !== delegationId) {
          return item;
        }
        matched = true;
        let noteFound = false;
        const activities = item.activities.map((activity): LiveActivity => {
          if (activity.kind !== 'note' || activity.id !== noteId) {
            return activity;
          }
          noteFound = true;
          return {
            ...activity,
            text: replace ? message.text : activity.text + message.text,
          };
        });
        return {
          ...item,
          activities: noteFound
            ? activities
            : [...activities, { kind: 'note', id: noteId, text: message.text }],
        };
      }),
    );
    if (!matched) {
      this.liveActivity.update((items) => [
        ...items,
        {
          kind: 'subagent',
          id: delegationId,
          name: message.agentName || 'subagent',
          label: message.agentLabel || formatAgentName(message.agentName),
          task: '',
          status: 'running',
          activities: [{ kind: 'note', id: noteId, text: message.text }],
        },
      ]);
    }
  }

  private failRunningSubagents(): void {
    this.liveActivity.update((items) =>
      items.map((item): LiveActivity =>
        item.kind === 'subagent' &&
        (item.status === 'running' ||
          item.status === 'approval_required' ||
          item.status === 'input_required')
          ? { ...item, status: 'failed' }
          : item,
      ),
    );
  }

  private mapLiveTools(
    items: readonly LiveActivity[],
    update: (item: LiveToolActivity) => LiveToolActivity,
  ): LiveActivity[] {
    return items.map((item): LiveActivity => {
      if (item.kind === 'subagent') {
        return { ...item, activities: this.mapLiveTools(item.activities, update) };
      }
      return item.kind === 'tool' ? update(item) : item;
    });
  }

  private updateMCPActivity(toolCallId: string, changes: { cancelling: boolean }): void {
    this.liveActivity.update(
      (items) =>
        updateLiveToolActivity(items, toolCallId, (item) =>
          item.mcp ? { ...item, mcp: { ...item.mcp, ...changes } } : item,
        ).items,
    );
  }

  private someLiveTool(
    items: readonly LiveActivity[],
    predicate: (item: LiveToolActivity) => boolean,
  ): boolean {
    return items.some((item) =>
      item.kind === 'subagent'
        ? this.someLiveTool(item.activities, predicate)
        : item.kind === 'tool' && predicate(item),
    );
  }

  private updateToolApproval(
    approvalId: string,
    changes: {
      status: 'pending' | 'submitting' | 'approved' | 'executing' | 'denied';
      reason?: string;
    },
  ): void {
    this.liveActivity.update((items) =>
      this.mapLiveTools(items, (item) =>
        item.approval?.id === approvalId
          ? {
              ...item,
              approval: {
                ...item.approval,
                ...changes,
                status:
                  item.approval.status === 'executing' && changes.status === 'approved'
                    ? 'executing'
                    : changes.status,
              },
            }
          : item,
      ),
    );
  }

  private updateUserInput(
    requestId: string,
    changes: {
      status: 'pending' | 'submitting' | 'answered';
      answers?: UserInputResolution['answers'];
    },
  ): void {
    this.liveActivity.update((items) =>
      this.mapLiveTools(items, (item) =>
        item.userInput?.id === requestId
          ? { ...item, userInput: { ...item.userInput, ...changes } }
          : item,
      ),
    );
  }

  private updateMCPElicitation(
    requestId: string,
    changes: { status: 'pending' | 'submitting' },
  ): void {
    this.liveActivity.update((items) =>
      this.mapLiveTools(items, (item) =>
        item.mcpElicitation?.id === requestId
          ? { ...item, mcpElicitation: { ...item.mcpElicitation, ...changes } }
          : item,
      ),
    );
  }

  private syncSessionWaitingStatus(): void {
    const run = this.activeRun();
    if (!run) {
      return;
    }
    const waitingForUser = this.someLiveTool(
      this.liveActivity(),
      (item) =>
        item.approval?.status === 'pending' ||
        item.approval?.status === 'submitting' ||
        item.userInput?.status === 'pending' ||
        item.userInput?.status === 'submitting' ||
        item.mcpElicitation?.status === 'pending' ||
        item.mcpElicitation?.status === 'submitting',
    );
    this.state.setSessionStatus(run.sessionId, waitingForUser ? 'waiting' : 'running');
  }

  private closeStream(): void {
    this.eventSource?.close();
    this.eventSource = null;
    this.streamConnected.set(false);
  }

  private scrollToBottom(): void {
    const anchor = this.transcriptEnd()?.nativeElement;
    if (anchor && typeof anchor.scrollIntoView === 'function') {
      anchor.scrollIntoView({ block: 'end' });
      return;
    }
    const element = this.scrollRegion()?.nativeElement;
    if (element) {
      element.scrollTop = element.scrollHeight;
    }
  }

  private isNearChatBottom(): boolean {
    const element = this.scrollRegion()?.nativeElement;
    return !element || element.scrollHeight - element.scrollTop - element.clientHeight < 96;
  }

  private scheduleScrollToBottom(shouldScroll: boolean): void {
    if (!shouldScroll || this.chatScrollFrame !== null) {
      return;
    }
    this.chatScrollFrame = window.requestAnimationFrame(() => {
      this.scrollToBottom();
      this.chatScrollFrame = null;
    });
  }

  private showError(error: unknown): void {
    this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
  }
}

function formatAgentName(name: string | undefined): string {
  const value = name?.replaceAll(/[_-]+/g, ' ').trim();
  if (!value) {
    return 'Sub-agent';
  }
  return `${value[0].toUpperCase()}${value.slice(1)}`;
}

function mcpLogNeedsAttention(level: string): boolean {
  return ['warning', 'error', 'critical', 'alert', 'emergency'].includes(
    level.trim().toLowerCase(),
  );
}

function mcpLogText(data: unknown): string {
  if (typeof data === 'string') {
    return data;
  }
  try {
    return JSON.stringify(data);
  } catch {
    return String(data);
  }
}

async function supportedFileType(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (
    bytes.length >= 8 &&
    bytes[0] === 0x89 &&
    bytes[1] === 0x50 &&
    bytes[2] === 0x4e &&
    bytes[3] === 0x47 &&
    bytes[4] === 0x0d &&
    bytes[5] === 0x0a &&
    bytes[6] === 0x1a &&
    bytes[7] === 0x0a
  ) {
    return 'image/png';
  }
  if (bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) {
    return 'image/jpeg';
  }
  const header = new TextDecoder('ascii').decode(bytes.subarray(0, 12));
  if (header.startsWith('GIF87a') || header.startsWith('GIF89a')) {
    return 'image/gif';
  }
  if (header.startsWith('RIFF') && header.slice(8, 12) === 'WEBP') {
    return 'image/webp';
  }
  if (header.startsWith('%PDF-')) {
    return 'application/pdf';
  }
  const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  if (text.includes('\u0000')) {
    throw new Error('binary file');
  }
  const declaredType = file.type.split(';', 1)[0].trim().toLowerCase();
  return isTextFileType(declaredType) ? declaredType : 'text/plain';
}

function isTextFileType(mimeType: string): boolean {
  if (mimeType.startsWith('text/')) {
    return true;
  }
  if (!mimeType.startsWith('application/')) {
    return false;
  }
  const subtype = mimeType.slice('application/'.length);
  return (
    ['json', 'javascript', 'sql', 'toml', 'xml', 'yaml', 'x-javascript', 'x-sh', 'x-yaml'].includes(
      subtype,
    ) ||
    subtype.endsWith('+json') ||
    subtype.endsWith('+xml')
  );
}

function resourceFiles(resource: import('../core/models').McpResourceRead): File[] {
  return resource.contents.map((content, index) => {
    const mimeType =
      content.mimeType || (content.text !== undefined ? 'text/plain' : 'application/octet-stream');
    const data = content.blob ? decodeBase64(content.blob) : (content.text ?? '');
    return new File([data], resourceFileName(content.uri || resource.uri, index), {
      type: mimeType,
    });
  });
}

function promptComposerContext(prompt: import('../core/models').McpPromptExpansion): {
  text: string;
  files: File[];
} {
  const text: string[] = [];
  const files: File[] = [];
  for (const [index, message] of prompt.messages.entries()) {
    const content = message.content;
    const type = typeof content['type'] === 'string' ? content['type'] : '';
    if (type === 'text' && typeof content['text'] === 'string') {
      const value = content['text'].trim();
      if (value) {
        text.push(prompt.messages.length === 1 ? value : `**${message.role}:**\n${value}`);
      }
      continue;
    }
    if (
      (type === 'image' || type === 'audio') &&
      typeof content['data'] === 'string' &&
      typeof content['mimeType'] === 'string'
    ) {
      files.push(
        new File([decodeBase64(content['data'])], `${prompt.name}-${index + 1}`, {
          type: content['mimeType'],
        }),
      );
      continue;
    }
    const embedded = recordContent(content['resource']);
    if (type === 'resource' && embedded) {
      const mimeType = stringContent(embedded['mimeType']) || 'text/plain';
      const data = stringContent(embedded['blob'])
        ? decodeBase64(stringContent(embedded['blob']))
        : stringContent(embedded['text']);
      files.push(
        new File([data], resourceFileName(stringContent(embedded['uri']), index), {
          type: mimeType,
        }),
      );
      continue;
    }
    if (type === 'resource_link' && typeof content['uri'] === 'string') {
      text.push(`[${stringContent(content['title']) || content['uri']}](${content['uri']})`);
    }
  }
  return { text: text.join('\n\n'), files };
}

function decodeBase64(value: string): ArrayBuffer {
  const decoded = window.atob(value);
  const buffer = new ArrayBuffer(decoded.length);
  const bytes = new Uint8Array(buffer);
  for (let index = 0; index < decoded.length; index++) {
    bytes[index] = decoded.charCodeAt(index);
  }
  return buffer;
}

function resourceFileName(uri: string, index: number): string {
  try {
    const parsed = new URL(uri);
    const name = decodeURIComponent(parsed.pathname.split('/').filter(Boolean).at(-1) ?? '');
    if (name) {
      return name;
    }
  } catch {
    // Non-URL resource identifiers are valid MCP URIs.
  }
  return `mcp-resource-${index + 1}`;
}

function recordContent(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function stringContent(value: unknown): string {
  return typeof value === 'string' ? value : '';
}
