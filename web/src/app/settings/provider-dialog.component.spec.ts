import { Signal, WritableSignal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { LlmProviderAuthType } from '../core/models';
import {
  ProviderDialogComponent,
  ProviderDialogData,
  ProviderDialogResult,
} from './provider-dialog.component';

interface ProviderDialogHarness {
  model: WritableSignal<{
    name: string;
    apiCompatibility: string;
    baseUrl: string;
    authType: LlmProviderAuthType;
    bearerTokenEnvVar: string;
    bearerToken: string;
  }>;
  providerForm: Signal<{ invalid: () => boolean }>;
  submit(event: SubmitEvent): void;
}

describe('ProviderDialogComponent', () => {
  const close = vi.fn();

  beforeEach(() => {
    close.mockReset();
  });

  it('requires a token for a new keyring provider and returns it write-only', async () => {
    const fixture = await createFixture(null);
    const component = fixture.componentInstance as unknown as ProviderDialogHarness;

    expect(component.providerForm().invalid()).toBe(true);
    component.model.update((value) => ({ ...value, name: 'Gateway', bearerToken: 'secret' }));
    fixture.detectChanges();

    expect(component.providerForm().invalid()).toBe(false);
    component.submit(new SubmitEvent('submit'));
    expect(close).toHaveBeenCalledWith({
      name: 'Gateway',
      apiCompatibility: 'anthropic',
      baseUrl: '',
      authType: 'bearer_keyring',
      bearerTokenEnvVar: '',
      bearerToken: 'secret',
    } satisfies ProviderDialogResult);
  });

  it('keeps an existing keyring token when the replacement field is blank', async () => {
    const fixture = await createFixture({
      id: 'provider-1',
      name: 'Gateway',
      apiCompatibility: 'anthropic',
      baseUrl: 'https://gateway.example.test',
      authType: 'bearer_keyring',
      bearerTokenEnvVar: '',
      credentialAvailable: true,
    });
    const component = fixture.componentInstance as unknown as ProviderDialogHarness;

    expect(component.providerForm().invalid()).toBe(false);
    component.submit(new SubmitEvent('submit'));
    expect(close).toHaveBeenCalledWith(
      expect.objectContaining({
        authType: 'bearer_keyring',
        bearerToken: '',
      }),
    );
  });

  it('requires an environment variable only for environment authentication', async () => {
    const fixture = await createFixture(null);
    const component = fixture.componentInstance as unknown as ProviderDialogHarness;
    component.model.set({
      name: 'Gateway',
      apiCompatibility: 'anthropic',
      baseUrl: '',
      authType: 'bearer_env',
      bearerTokenEnvVar: '',
      bearerToken: '',
    });
    fixture.detectChanges();

    expect(component.providerForm().invalid()).toBe(true);
    component.model.update((value) => ({
      ...value,
      bearerTokenEnvVar: 'GATEWAY_TOKEN',
    }));
    fixture.detectChanges();

    expect(component.providerForm().invalid()).toBe(false);
  });

  it('uses API key terminology for Gemini credentials', async () => {
    const fixture = await createFixture(null);
    const component = fixture.componentInstance as unknown as ProviderDialogHarness;
    component.model.update((value) => ({
      ...value,
      apiCompatibility: 'gemini',
    }));
    fixture.detectChanges();

    const labels = Array.from(
      fixture.nativeElement.querySelectorAll('mat-label') as NodeListOf<HTMLElement>,
      (element) => element.textContent?.trim(),
    );
    expect(labels).toContain('API key');
    const credentialInput = fixture.nativeElement.querySelector(
      'input[type="password"]',
    ) as HTMLInputElement | null;
    expect(credentialInput).not.toBeNull();
  });

  async function createFixture(
    data: ProviderDialogData,
  ): Promise<ComponentFixture<ProviderDialogComponent>> {
    await TestBed.configureTestingModule({
      imports: [ProviderDialogComponent],
      providers: [
        { provide: MAT_DIALOG_DATA, useValue: data },
        { provide: MatDialogRef, useValue: { close } },
      ],
    }).compileComponents();
    const fixture = TestBed.createComponent(ProviderDialogComponent);
    fixture.detectChanges();
    await fixture.whenStable();
    return fixture;
  }
});
