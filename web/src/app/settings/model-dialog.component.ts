import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { FormField, form, min, required, validate } from '@angular/forms/signals';
import { MatAutocompleteModule } from '@angular/material/autocomplete';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSelectModule } from '@angular/material/select';
import { MatSnackBar } from '@angular/material/snack-bar';
import { MatTooltipModule } from '@angular/material/tooltip';

import { ApiService, errorMessage } from '../core/api.service';
import { AvailableLlmModel, LlmModel, LlmModelInput, LlmProvider } from '../core/models';
import { reasoningEffortOptionsForCompatibility } from '../core/reasoning-effort';

export interface ModelDialogData {
  model: LlmModel | null;
  providers: LlmProvider[];
  initialProviderId?: string;
}

export type ModelDialogResult = LlmModelInput;

@Component({
  selector: 'app-model-dialog',
  imports: [
    FormField,
    MatAutocompleteModule,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    MatSelectModule,
    MatTooltipModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.model ? 'Edit model' : 'Add model' }}</h2>
    <form (submit)="submit($event)">
      <mat-dialog-content>
        <mat-form-field class="wide-field" appearance="outline">
          <mat-label>Provider</mat-label>
          <mat-select
            [formField]="modelForm.llmProviderId"
            (selectionChange)="providerChanged($event.value)"
          >
            @for (provider of data.providers; track provider.id) {
              <mat-option [value]="provider.id">{{ provider.name }}</mat-option>
            }
          </mat-select>
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Name</mat-label>
          <input matInput [formField]="modelForm.name" autocomplete="off" />
          @if (modelForm.name().touched() && modelForm.name().invalid()) {
            <mat-error>Name is required</mat-error>
          }
        </mat-form-field>
        <mat-form-field appearance="outline">
          <mat-label>Model ID</mat-label>
          <input
            matInput
            [formField]="modelForm.modelId"
            autocomplete="off"
            spellcheck="false"
            [placeholder]="modelIdPlaceholder()"
            [matAutocomplete]="availableModelsAutocomplete"
          />
          @if (supportsModelDiscovery()) {
            <button
              mat-icon-button
              matSuffix
              type="button"
              [disabled]="availableModelsLoading()"
              [matTooltip]="
                availableModels().length > 0 ? 'Reload provider models' : 'Load provider models'
              "
              aria-label="Load provider models"
              (click)="loadAvailableModels()"
            >
              @if (availableModelsLoading()) {
                <mat-spinner diameter="20" />
              } @else {
                <mat-icon>refresh</mat-icon>
              }
            </button>
          }
          <mat-autocomplete
            #availableModelsAutocomplete="matAutocomplete"
            (optionSelected)="selectAvailableModel($event.option.value)"
          >
            @for (availableModel of filteredAvailableModels(); track availableModel.id) {
              <mat-option [value]="availableModel.id">
                {{ availableModel.displayName || availableModel.id }}
              </mat-option>
            }
          </mat-autocomplete>
          @if (modelForm.modelId().touched() && modelForm.modelId().invalid()) {
            <mat-error>Model ID is required</mat-error>
          }
        </mat-form-field>
        <div class="generation-grid wide-field">
          <mat-form-field appearance="outline">
            <mat-label>Context window</mat-label>
            <input matInput type="number" step="1" [formField]="modelForm.contextWindowTokens" />
            <mat-hint>Maximum input and output tokens</mat-hint>
            @if (
              modelForm.contextWindowTokens().touched() && modelForm.contextWindowTokens().invalid()
            ) {
              <mat-error>Use a whole number at least as large as the output limit</mat-error>
            }
          </mat-form-field>
          <mat-form-field appearance="outline">
            <mat-label>Max output tokens</mat-label>
            <input matInput type="number" step="1" [formField]="modelForm.maxOutputTokens" />
            @if (modelForm.maxOutputTokens().touched() && modelForm.maxOutputTokens().invalid()) {
              <mat-error>Use a positive whole number</mat-error>
            }
          </mat-form-field>
          @if (supportsReasoningEffort()) {
            <mat-form-field appearance="outline">
              <mat-label>Reasoning effort</mat-label>
              <mat-select [formField]="modelForm.reasoningEffort">
                <mat-option [value]="null">Provider default</mat-option>
                @for (option of reasoningEffortOptions(); track option.value) {
                  <mat-option [value]="option.value">{{ option.label }}</mat-option>
                }
              </mat-select>
            </mat-form-field>
          }
        </div>
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close>Cancel</button>
        <button mat-flat-button type="submit" [disabled]="modelForm().invalid()">
          {{ data.model ? 'Save' : 'Add' }}
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styles: `
    mat-dialog-content {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 0 12px;
      min-width: min(560px, 76vw);
      padding-top: 8px;
    }

    .wide-field {
      grid-column: 1 / -1;
    }

    .generation-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }

    @media (max-width: 640px) {
      mat-dialog-content,
      .generation-grid {
        grid-template-columns: minmax(0, 1fr);
      }

      mat-dialog-content {
        min-width: 0;
      }
    }
  `,
})
export class ModelDialogComponent implements OnInit {
  protected readonly data = inject<ModelDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<ModelDialogComponent>);
  private readonly api = inject(ApiService);
  private readonly snackBar = inject(MatSnackBar);
  private availableModelsRequest = 0;
  private readonly model = signal({
    llmProviderId:
      this.data.model?.llmProviderId ??
      this.data.initialProviderId ??
      this.data.providers[0]?.id ??
      '',
    name: this.data.model?.name ?? '',
    modelId: this.data.model?.modelId ?? '',
    contextWindowTokens: this.data.model?.contextWindowTokens ?? 128_000,
    maxOutputTokens: this.data.model?.maxOutputTokens ?? 4096,
    reasoningEffort: this.data.model?.reasoningEffort ?? null,
  });
  private readonly selectedProvider = computed(() =>
    this.data.providers.find((provider) => provider.id === this.model().llmProviderId),
  );
  protected readonly reasoningEffortOptions = computed(() =>
    reasoningEffortOptionsForCompatibility(this.selectedProvider()?.apiCompatibility),
  );
  protected readonly supportsReasoningEffort = computed(
    () => this.reasoningEffortOptions().length > 0,
  );
  protected readonly supportsModelDiscovery = computed(() =>
    this.providerSupportsModelDiscovery(this.selectedProvider()),
  );
  protected readonly availableModels = signal<AvailableLlmModel[]>([]);
  protected readonly availableModelsLoading = signal(false);
  protected readonly filteredAvailableModels = computed(() => {
    const query = this.model().modelId.trim().toLowerCase();
    const models = this.availableModels();
    if (query === '') {
      return models;
    }
    return models.filter(
      (item) =>
        item.id.toLowerCase().includes(query) || item.displayName?.toLowerCase().includes(query),
    );
  });
  protected readonly modelIdPlaceholder = computed(() => {
    switch (this.selectedProvider()?.apiCompatibility) {
      case 'gemini':
        return 'gemini-2.5-flash';
      case 'openai-chat-completions':
      case 'openai-responses':
        return 'gpt-4.1-mini';
      default:
        return 'claude-sonnet-4-20250514';
    }
  });
  protected readonly modelForm = form(this.model, (path) => {
    required(path.llmProviderId);
    required(path.name);
    required(path.modelId);
    required(path.contextWindowTokens);
    min(path.contextWindowTokens, 1);
    required(path.maxOutputTokens);
    min(path.maxOutputTokens, 1);
    validate(path.contextWindowTokens, ({ value, valueOf }) => {
      const contextWindowTokens = value();
      const maxOutputTokens = valueOf(path.maxOutputTokens);
      if (Number.isInteger(contextWindowTokens) && contextWindowTokens >= maxOutputTokens) {
        return undefined;
      }
      return {
        kind: 'contextWindow',
        message: 'Context window must be a whole number at least as large as max output tokens',
      };
    });
    validate(path.maxOutputTokens, ({ value }) =>
      Number.isInteger(value())
        ? undefined
        : { kind: 'integer', message: 'Max output tokens must be a whole number' },
    );
  });

  ngOnInit(): void {
    if (this.supportsModelDiscovery()) {
      void this.loadAvailableModels();
    }
  }

  protected providerChanged(providerId: string): void {
    this.availableModelsRequest++;
    this.availableModels.set([]);
    this.availableModelsLoading.set(false);
    const provider = this.data.providers.find((item) => item.id === providerId);
    const effortOptions = reasoningEffortOptionsForCompatibility(provider?.apiCompatibility);
    this.model.update((value) =>
      value.reasoningEffort &&
      !effortOptions.some((option) => option.value === value.reasoningEffort)
        ? { ...value, reasoningEffort: null }
        : value,
    );
    if (this.providerSupportsModelDiscovery(provider)) {
      void this.loadAvailableModels(providerId);
    }
  }

  protected async loadAvailableModels(
    providerId: string = this.model().llmProviderId,
  ): Promise<void> {
    const request = ++this.availableModelsRequest;
    this.availableModelsLoading.set(true);
    try {
      const models = await this.api.listAvailableLlmModels(providerId);
      if (request === this.availableModelsRequest) {
        this.availableModels.set(models);
      }
    } catch (error) {
      if (request === this.availableModelsRequest) {
        this.snackBar.open(errorMessage(error), 'Dismiss', { duration: 7000 });
      }
    } finally {
      if (request === this.availableModelsRequest) {
        this.availableModelsLoading.set(false);
      }
    }
  }

  protected selectAvailableModel(modelId: string): void {
    const availableModel = this.availableModels().find((item) => item.id === modelId);
    this.model.update((value) => ({
      ...value,
      name:
        value.name.trim() || availableModel?.displayName?.trim() || this.deriveModelName(modelId),
      contextWindowTokens:
        availableModel?.contextWindowTokens && availableModel.contextWindowTokens > 0
          ? availableModel.contextWindowTokens
          : value.contextWindowTokens,
      maxOutputTokens:
        availableModel?.maxOutputTokens && availableModel.maxOutputTokens > 0
          ? availableModel.maxOutputTokens
          : value.maxOutputTokens,
    }));
  }

  private providerSupportsModelDiscovery(provider: LlmProvider | undefined): boolean {
    return (
      provider?.apiCompatibility === 'anthropic' ||
      provider?.apiCompatibility === 'gemini' ||
      provider?.apiCompatibility === 'openai-chat-completions' ||
      provider?.apiCompatibility === 'openai-responses'
    );
  }

  private deriveModelName(modelId: string): string {
    const identifier = modelId.split('/').at(-1) ?? modelId;
    return identifier
      .split(/[-_]+/)
      .filter(Boolean)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join(' ');
  }

  protected submit(event: SubmitEvent): void {
    event.preventDefault();
    if (this.modelForm().invalid()) {
      return;
    }
    const value = this.model();
    this.dialogRef.close({
      llmProviderId: value.llmProviderId,
      name: value.name.trim(),
      modelId: value.modelId.trim(),
      contextWindowTokens: Number(value.contextWindowTokens),
      maxOutputTokens: Number(value.maxOutputTokens),
      reasoningEffort: this.supportsReasoningEffort() ? value.reasoningEffort : null,
    } satisfies ModelDialogResult);
  }
}
