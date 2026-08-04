import { Signal, WritableSignal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';
import { MatSnackBar } from '@angular/material/snack-bar';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiService } from '../core/api.service';
import { AvailableLlmModel, LlmProvider } from '../core/models';
import { ModelDialogComponent, ModelDialogData } from './model-dialog.component';

describe('ModelDialogComponent', () => {
  let fixture: ComponentFixture<ModelDialogComponent>;
  const availableModels: AvailableLlmModel[] = [
    {
      id: 'gpt-5.2',
      displayName: 'GPT-5.2',
      contextWindowTokens: 400_000,
      maxOutputTokens: 128_000,
    },
  ];
  const provider: LlmProvider = {
    id: 'provider-1',
    name: 'OpenAI Responses',
    apiCompatibility: 'openai-responses',
    baseUrl: 'https://api.example.test/v1',
    authType: 'bearer_env',
    bearerTokenEnvVar: 'TEST_TOKEN',
    credentialAvailable: true,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
  };
  const anthropicProvider: LlmProvider = {
    ...provider,
    id: 'provider-anthropic',
    name: 'Anthropic Messages',
    apiCompatibility: 'anthropic',
  };
  const geminiProvider: LlmProvider = {
    ...provider,
    id: 'provider-gemini',
    name: 'Gemini',
    apiCompatibility: 'gemini',
  };
  const api = {
    listAvailableLlmModels: vi.fn<(providerId: string) => Promise<AvailableLlmModel[]>>(),
  };

  beforeEach(async () => {
    api.listAvailableLlmModels.mockReset().mockResolvedValue(availableModels);
    await TestBed.configureTestingModule({
      imports: [ModelDialogComponent],
      providers: [
        { provide: ApiService, useValue: api },
        { provide: MatDialogRef, useValue: { close: vi.fn() } },
        { provide: MatSnackBar, useValue: { open: vi.fn() } },
        {
          provide: MAT_DIALOG_DATA,
          useValue: {
            model: null,
            providers: [provider, anthropicProvider, geminiProvider],
            initialProviderId: provider.id,
          } satisfies ModelDialogData,
        },
      ],
    }).compileComponents();
    fixture = TestBed.createComponent(ModelDialogComponent);
  });

  it('loads Responses models and shows only their supported generation settings', async () => {
    fixture.detectChanges();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(api.listAvailableLlmModels).toHaveBeenCalledWith(provider.id);
    const component = fixture.componentInstance as unknown as {
      availableModels: Signal<AvailableLlmModel[]>;
      model: Signal<{ contextWindowTokens: number; maxOutputTokens: number }>;
      selectAvailableModel(modelId: string): void;
    };
    expect(component.availableModels()).toEqual(availableModels);

    component.selectAvailableModel(availableModels[0].id);
    fixture.detectChanges();
    const nameInput = fixture.nativeElement.querySelector('input') as HTMLInputElement | null;
    expect(nameInput?.value).toBe('GPT-5.2');
    expect(fixture.nativeElement.querySelector('textarea')).toBeNull();
    const labels = Array.from(
      fixture.nativeElement.querySelectorAll('mat-label') as NodeListOf<HTMLElement>,
      (element) => element.textContent?.trim(),
    );
    expect(labels).toContain('Reasoning effort');
    expect(labels).toContain('Context window');
    expect(labels).not.toContain('Temperature');
    expect(labels).not.toContain('Top P');
    expect(labels).not.toContain('Top K');
    expect(labels).not.toContain('Stop sequences');
    expect(component.model().contextWindowTokens).toBe(400_000);
    expect(component.model().maxOutputTokens).toBe(128_000);
  });

  it('clears an effort level that the newly selected provider does not support', () => {
    fixture.detectChanges();
    const component = fixture.componentInstance as unknown as {
      model: WritableSignal<{
        llmProviderId: string;
        reasoningEffort: 'ultra' | null;
      }>;
      providerChanged(providerId: string): void;
      reasoningEffortOptions: Signal<readonly { value: string }[]>;
    };
    component.model.update((value) => ({
      ...value,
      llmProviderId: anthropicProvider.id,
      reasoningEffort: 'ultra',
    }));

    component.providerChanged(anthropicProvider.id);

    expect(component.model().reasoningEffort).toBeNull();
    expect(component.reasoningEffortOptions().map((option) => option.value)).toEqual([
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ]);
  });

  it('discovers Gemini models without exposing reasoning effort', async () => {
    fixture.detectChanges();
    const component = fixture.componentInstance as unknown as {
      model: WritableSignal<{
        llmProviderId: string;
        reasoningEffort: 'high' | null;
      }>;
      providerChanged(providerId: string): void;
      modelIdPlaceholder: Signal<string>;
      supportsReasoningEffort: Signal<boolean>;
    };
    component.model.update((value) => ({
      ...value,
      llmProviderId: geminiProvider.id,
      reasoningEffort: 'high',
    }));

    component.providerChanged(geminiProvider.id);
    await fixture.whenStable();

    expect(api.listAvailableLlmModels).toHaveBeenCalledWith(geminiProvider.id);
    expect(component.model().reasoningEffort).toBeNull();
    expect(component.modelIdPlaceholder()).toBe('gemini-2.5-flash');
    expect(component.supportsReasoningEffort()).toBe(false);
  });
});
