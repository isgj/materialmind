import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  McpToolPermissionsDialogComponent,
  McpToolPermissionsDialogData,
} from './mcp-tool-permissions-dialog.component';

describe('McpToolPermissionsDialogComponent', () => {
  let fixture: ComponentFixture<McpToolPermissionsDialogComponent>;
  const close = vi.fn();

  beforeEach(async () => {
    close.mockReset();
    await TestBed.configureTestingModule({
      imports: [McpToolPermissionsDialogComponent],
      providers: [
        { provide: MatDialogRef, useValue: { close } },
        {
          provide: MAT_DIALOG_DATA,
          useValue: {
            serverName: 'Browser tools default',
            defaultConfirmation: 'ask',
            tools: [{ name: 'open_page', description: 'Open a page' }],
            permissions: [],
          } satisfies McpToolPermissionsDialogData,
        },
      ],
    }).compileComponents();
    fixture = TestBed.createComponent(McpToolPermissionsDialogComponent);
    fixture.detectChanges();
  });

  it('closes without a result when cancelled', () => {
    const cancel = fixture.nativeElement.querySelector('button') as HTMLButtonElement;

    cancel.click();

    expect(close).toHaveBeenCalledOnce();
    expect(close).toHaveBeenCalledWith();
  });

  it('returns an empty permission list when applied without overrides', () => {
    const buttons = fixture.nativeElement.querySelectorAll(
      'button',
    ) as NodeListOf<HTMLButtonElement>;

    buttons[1].click();

    expect(close).toHaveBeenCalledWith([]);
  });
});
