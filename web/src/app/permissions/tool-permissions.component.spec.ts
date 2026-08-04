import { signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MatSelect } from '@angular/material/select';
import { MatSnackBar } from '@angular/material/snack-bar';
import { By } from '@angular/platform-browser';
import { ActivatedRoute, convertToParamMap, provideRouter } from '@angular/router';
import { of } from 'rxjs';

import { ApiService } from '../core/api.service';
import { AppState } from '../core/app-state.service';
import { McpServerAssignment, ToolPermissionSet } from '../core/models';
import { ToolPermissionsComponent } from './tool-permissions.component';

describe('ToolPermissionsComponent', () => {
  let fixture: ComponentFixture<ToolPermissionsComponent>;
  let api: {
    getSessionToolPermissions: ReturnType<typeof vi.fn>;
    replaceSessionToolPermissions: ReturnType<typeof vi.fn>;
    getSessionMcpServers: ReturnType<typeof vi.fn>;
    replaceSessionMcpServers: ReturnType<typeof vi.fn>;
    listMcpServerTools: ReturnType<typeof vi.fn>;
  };
  let selectWorkspace: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    const paramMap = convertToParamMap({ sessionId: 'session-1' });
    api = {
      getSessionToolPermissions: vi.fn().mockResolvedValue(permissionSet()),
      replaceSessionToolPermissions: vi.fn(),
      getSessionMcpServers: vi.fn().mockResolvedValue([]),
      replaceSessionMcpServers: vi.fn(),
      listMcpServerTools: vi.fn().mockResolvedValue({ protocolVersion: '2026-07-28', tools: [] }),
    };
    selectWorkspace = vi.fn().mockResolvedValue(undefined);

    TestBed.configureTestingModule({
      providers: [
        provideRouter([]),
        { provide: ApiService, useValue: api },
        {
          provide: AppState,
          useValue: {
            allSessions: signal([]),
            selectWorkspace,
          },
        },
        {
          provide: ActivatedRoute,
          useValue: {
            paramMap: of(paramMap),
            snapshot: {
              data: { ownerType: 'session' },
              paramMap,
            },
          },
        },
        { provide: MatSnackBar, useValue: { open: vi.fn() } },
      ],
    });
  });

  it('loads the session policy and explains its workspace snapshot', async () => {
    fixture = TestBed.createComponent(ToolPermissionsComponent);
    await fixture.whenStable();
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(api.getSessionToolPermissions).toHaveBeenCalledWith('session-1');
    expect(selectWorkspace).toHaveBeenCalledWith('workspace-1');
    expect(root.querySelectorAll('tr.mat-mdc-row')).toHaveLength(7);
    expect(root.textContent).toContain('New sessions copy settings from Project when created;');
    const selectValues = fixture.debugElement
      .queryAll(By.directive(MatSelect))
      .map((element) => (element.componentInstance as MatSelect).value);
    expect(selectValues).toContain('allow');
    expect(selectValues).toContain('ask');
    expect(selectValues).toContain('repository');
    expect(root.textContent).toContain('Manage');
    expect(root.textContent).toContain('commands are not sandboxed');
  });

  it('treats null capability arrays from an older backend as empty', async () => {
    const response = permissionSet();
    response.definitions[0].supportedTargetMatchers = null as never;
    api.getSessionToolPermissions.mockResolvedValue(response);

    fixture = TestBed.createComponent(ToolPermissionsComponent);
    await fixture.whenStable();
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelectorAll('tr.mat-mdc-row')).toHaveLength(7);
    expect(root.textContent).toContain('Not applicable');
  });

  it('shows inherited MCP servers with their confirmation policy', async () => {
    api.getSessionMcpServers.mockResolvedValue([mcpAssignment()]);

    fixture = TestBed.createComponent(ToolPermissionsComponent);
    await fixture.whenStable();
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(api.getSessionMcpServers).toHaveBeenCalledWith('session-1');
    expect(root.textContent).toContain('Project context');
    expect(root.textContent).toContain('Streamable HTTP server');
    expect(
      fixture.debugElement
        .queryAll(By.directive(MatSelect))
        .map((element) => (element.componentInstance as MatSelect).value),
    ).toContain('ask');
    expect(root.textContent).toContain('1');
    expect(root.textContent).toContain('overrides');
  });
});

function mcpAssignment(): McpServerAssignment {
  return {
    server: {
      id: 'mcp-1',
      name: 'Project context',
      transport: 'http',
      command: '',
      arguments: [],
      environment: [],
      url: 'https://mcp.example.com/mcp',
      headers: [],
      authType: 'none',
      bearerTokenEnvVar: '',
      oauthClientMode: 'dynamic',
      oauthClientId: '',
      oauthClientSecretEnvVar: '',
      oauthScopes: [],
      defaultEnabled: false,
      defaultConfirmationMode: 'ask',
      defaultToolPermissions: [],
      available: true,
      credentialAvailable: true,
      createdAt: '2026-07-27T00:00:00Z',
      updatedAt: '2026-07-27T00:00:00Z',
    },
    enabled: true,
    confirmationMode: 'ask',
    toolPermissions: [{ toolName: 'search', confirmationMode: 'allow' }],
  };
}

function permissionSet(): ToolPermissionSet {
  return {
    ownerType: 'session',
    ownerId: 'session-1',
    ownerName: 'Review',
    workspaceId: 'workspace-1',
    workspaceName: 'Project',
    workspaceRoot: '/home/user/project',
    repositoryRoot: '/home/user',
    sessionStatus: 'idle',
    definitions: [
      {
        name: 'list_directory',
        label: 'List directory',
        description: 'Inspect files and directories without reading file contents.',
        defaultConfirmation: 'allow',
        defaultFilesystemScope: 'workspace',
        supportedScopes: ['workspace', 'repository', 'computer'],
        supportedTargetMatchers: [],
      },
      {
        name: 'read_file',
        label: 'Read file',
        description: 'Read bounded ranges from UTF-8 text files.',
        defaultConfirmation: 'allow',
        defaultFilesystemScope: 'workspace',
        supportedScopes: ['workspace', 'repository', 'computer'],
        supportedTargetMatchers: [],
      },
      {
        name: 'grep',
        label: 'Search files',
        description: 'Search file contents with ripgrep and return structured matches.',
        defaultConfirmation: 'allow',
        defaultFilesystemScope: 'workspace',
        supportedScopes: ['workspace', 'repository', 'computer'],
        supportedTargetMatchers: [],
      },
      {
        name: 'fetch_url',
        label: 'Fetch URL',
        description: 'Fetch bounded text content from public HTTP or HTTPS URLs.',
        defaultConfirmation: 'ask',
        supportedScopes: [],
        supportedTargetMatchers: ['exact_url', 'origin'],
      },
      {
        name: 'edit_file',
        label: 'Edit file',
        description: 'Create, update, or delete files with a reviewable patch.',
        defaultConfirmation: 'ask',
        defaultFilesystemScope: 'workspace',
        supportedScopes: ['workspace', 'repository', 'computer'],
        supportedTargetMatchers: [],
      },
      {
        name: 'load_skill',
        label: 'Load skill',
        description:
          'Load instructions or resources from a discovered workspace, parent, or global skill.',
        defaultConfirmation: 'allow',
        supportedScopes: [],
        supportedTargetMatchers: [],
      },
      {
        name: 'run_command',
        label: 'Run command',
        description:
          'Run a non-interactive command from an allowed workspace or repository directory.',
        defaultConfirmation: 'ask',
        defaultFilesystemScope: 'workspace',
        supportedScopes: ['workspace', 'repository'],
        supportedTargetMatchers: [],
      },
    ],
    permissions: [
      {
        toolName: 'list_directory',
        confirmationMode: 'allow',
        filesystemScope: 'workspace',
        targetRules: [],
      },
      {
        toolName: 'read_file',
        confirmationMode: 'allow',
        filesystemScope: 'repository',
        targetRules: [],
      },
      {
        toolName: 'grep',
        confirmationMode: 'allow',
        filesystemScope: 'workspace',
        targetRules: [],
      },
      {
        toolName: 'fetch_url',
        confirmationMode: 'ask',
        filesystemScope: '',
        targetRules: [],
      },
      {
        toolName: 'edit_file',
        confirmationMode: 'ask',
        filesystemScope: 'workspace',
        targetRules: [],
      },
      {
        toolName: 'load_skill',
        confirmationMode: 'allow',
        filesystemScope: '',
        targetRules: [],
      },
      {
        toolName: 'run_command',
        confirmationMode: 'ask',
        filesystemScope: 'repository',
        targetRules: [],
      },
    ],
  };
}
