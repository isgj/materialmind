import { ComponentFixture, TestBed } from '@angular/core/testing';

import { ApiService } from '../../core/api.service';
import { McpAppView } from './mcp-app-view';

describe('McpAppView', () => {
  let fixture: ComponentFixture<McpAppView>;
  const api = {
    readSessionMcpResource: vi.fn(),
  };

  beforeEach(async () => {
    api.readSessionMcpResource.mockReset().mockResolvedValue({
      serverId: 'server-1',
      serverName: 'Artifacts',
      uri: 'ui://artifacts/preview',
      contents: [
        {
          uri: 'ui://artifacts/preview',
          mimeType: 'text/html;profile=mcp-app',
          text: '<main>Artifact preview</main>',
          meta: {},
        },
      ],
    });
    await TestBed.configureTestingModule({
      imports: [McpAppView],
      providers: [{ provide: ApiService, useValue: api }],
    }).compileComponents();

    fixture = TestBed.createComponent(McpAppView);
    fixture.componentRef.setInput('sessionId', 'session-1');
    fixture.componentRef.setInput('details', {
      serverId: 'server-1',
      serverName: 'Artifacts',
      toolName: 'inspect',
      uiResourceUri: 'ui://artifacts/preview',
      cancelable: false,
      cancelling: false,
      logs: [],
      content: [],
      isError: false,
    });
    fixture.componentRef.setInput('arguments', { artifactId: 'artifact-1' });
    fixture.detectChanges();
    await fixture.whenStable();
    fixture.detectChanges();
  });

  it('loads the app resource in a script-only sandbox', () => {
    expect(api.readSessionMcpResource).toHaveBeenCalledOnce();
    expect(api.readSessionMcpResource).toHaveBeenCalledWith(
      'session-1',
      'server-1',
      'ui://artifacts/preview',
    );
    const frame = fixture.nativeElement.querySelector('iframe') as HTMLIFrameElement | null;
    expect(frame).not.toBeNull();
    expect(frame?.getAttribute('sandbox')).toBe('allow-scripts');
    expect(frame?.srcdoc).toContain('Content-Security-Policy');
    expect(frame?.srcdoc).toContain('Artifact preview');
  });

  it('keeps the app fallback inside the tool details when the resource is unavailable', async () => {
    api.readSessionMcpResource.mockRejectedValue(new Error('Resource unavailable'));
    fixture.componentRef.setInput('details', {
      ...fixture.componentRef.instance.details(),
      uiResourceUri: 'ui://artifacts/other-preview',
    });
    fixture.detectChanges();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(fixture.nativeElement.textContent).toContain('Resource unavailable');
  });
});
