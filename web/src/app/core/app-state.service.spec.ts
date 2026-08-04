import { TestBed } from '@angular/core/testing';

import { ApiService } from './api.service';
import { AppState } from './app-state.service';
import { AppSession } from './models';

describe('AppState', () => {
  const session: AppSession = {
    id: 'session-1',
    workspaceId: 'workspace-1',
    title: 'Review',
    runtimeType: 'adk',
    selectedLlmModelId: 'model-1',
    acpAgentId: null,
    acpConfigOptions: [],
    status: 'running',
    createdAt: '2026-07-20T12:00:00Z',
    updatedAt: '2026-07-20T12:01:00Z',
  };

  let nextSessions: AppSession[];
  let state: AppState;

  beforeEach(() => {
    nextSessions = [{ ...session }];
    TestBed.configureTestingModule({
      providers: [
        AppState,
        {
          provide: ApiService,
          useValue: {
            listAllSessions: () => Promise.resolve(nextSessions),
          },
        },
      ],
    });
    state = TestBed.inject(AppState);
  });

  it('preserves session identity when polling returns unchanged data', async () => {
    const current = [{ ...session }];
    state.allSessions.set(current);

    await state.refreshSessions();

    expect(state.allSessions()).toBe(current);
  });

  it('does not replace session state for a repeated stream status', () => {
    const current = [{ ...session }];
    state.allSessions.set(current);

    state.setSessionStatus(session.id, 'running');

    expect(state.allSessions()).toBe(current);
  });

  it('applies a stream waiting status immediately', () => {
    state.allSessions.set([{ ...session }]);

    state.setSessionStatus(session.id, 'waiting');

    expect(state.allSessions()[0]?.status).toBe('waiting');
  });

  it('reconciles a local waiting status with the backend runtime status', async () => {
    state.allSessions.set([{ ...session }]);
    state.setSessionStatus(session.id, 'waiting');

    await state.refreshSessions();

    expect(state.allSessions()[0]?.status).toBe('running');
  });

  it('clears a local waiting status when the backend session becomes idle', async () => {
    state.allSessions.set([{ ...session }]);
    state.setSessionStatus(session.id, 'waiting');
    nextSessions = [{ ...session, status: 'idle' }];

    await state.refreshSessions();

    expect(state.allSessions()[0]?.status).toBe('idle');
  });

  it('applies a waiting status returned for another session', async () => {
    nextSessions = [{ ...session, status: 'waiting' }];

    await state.refreshSessions();

    expect(state.allSessions()[0]?.status).toBe('waiting');
  });
});
