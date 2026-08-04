import { Routes } from '@angular/router';

export const routes: Routes = [
  {
    path: '',
    loadComponent: () => import('./shell/shell.component').then((module) => module.ShellComponent),
    children: [
      {
        path: '',
        pathMatch: 'full',
        loadComponent: () =>
          import('./overview/overview.component').then((module) => module.OverviewComponent),
      },
      {
        path: 'workspace/:workspaceId/permissions',
        loadComponent: () =>
          import('./permissions/tool-permissions.component').then(
            (module) => module.ToolPermissionsComponent,
          ),
        data: { ownerType: 'workspace' },
      },
      {
        path: 'workspace/:workspaceId',
        loadComponent: () =>
          import('./overview/overview.component').then((module) => module.OverviewComponent),
      },
      {
        path: 'session/:sessionId/permissions',
        loadComponent: () =>
          import('./permissions/tool-permissions.component').then(
            (module) => module.ToolPermissionsComponent,
          ),
        data: { ownerType: 'session' },
      },
      {
        path: 'session/:sessionId',
        loadComponent: () => import('./chat/chat.component').then((module) => module.ChatComponent),
      },
      {
        path: 'settings',
        pathMatch: 'full',
        redirectTo: 'settings/appearance',
      },
      {
        path: 'settings/appearance',
        loadComponent: () =>
          import('./settings/settings.component').then((module) => module.SettingsComponent),
        data: { section: 'appearance' },
      },
      {
        path: 'settings/runtimes',
        loadComponent: () =>
          import('./settings/settings.component').then((module) => module.SettingsComponent),
        data: { section: 'runtimes' },
      },
      {
        path: 'settings/mcp',
        loadComponent: () =>
          import('./settings/settings.component').then((module) => module.SettingsComponent),
        data: { section: 'mcp' },
      },
      {
        path: 'settings/models',
        loadComponent: () =>
          import('./settings/settings.component').then((module) => module.SettingsComponent),
        data: { section: 'models' },
      },
      {
        path: 'settings/data',
        loadComponent: () =>
          import('./settings/settings.component').then((module) => module.SettingsComponent),
        data: { section: 'data' },
      },
    ],
  },
  { path: '**', redirectTo: '' },
];
