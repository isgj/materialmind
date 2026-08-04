package store

import (
	"context"
	"database/sql"
	"fmt"

	"materialmind/internal/toolpolicy"
)

type permissionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) GetWorkspaceToolPermissions(ctx context.Context, workspaceID string) ([]toolpolicy.Permission, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return nil, err
	}
	return loadWorkspaceToolPermissions(ctx, s.db, workspaceID)
}

func (s *Store) ReplaceWorkspaceToolPermissions(ctx context.Context, workspaceID string, permissions []toolpolicy.Permission) ([]toolpolicy.Permission, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return nil, err
	}
	normalized, err := normalizeToolPermissions(permissions)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin workspace tool permission update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_tool_permissions WHERE workspace_id = ?`, workspaceID); err != nil {
		return nil, fmt.Errorf("clear workspace tool permissions: %w", err)
	}
	if err := insertWorkspaceToolPermissions(ctx, tx, workspaceID, normalized); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit workspace tool permission update: %w", err)
	}
	return normalized, nil
}

func (s *Store) GetSessionToolPermissions(ctx context.Context, sessionID string) ([]toolpolicy.Permission, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	return loadSessionToolPermissions(ctx, s.db, sessionID)
}

func (s *Store) ReplaceSessionToolPermissions(ctx context.Context, sessionID string, permissions []toolpolicy.Permission) ([]toolpolicy.Permission, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	normalized, err := normalizeToolPermissions(permissions)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session tool permission update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_tool_permissions WHERE session_id = ?`, sessionID); err != nil {
		return nil, fmt.Errorf("clear session tool permissions: %w", err)
	}
	if err := insertSessionToolPermissions(ctx, tx, sessionID, normalized); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session tool permission update: %w", err)
	}
	return normalized, nil
}

func loadWorkspaceToolPermissions(ctx context.Context, queryer permissionQueryer, workspaceID string) ([]toolpolicy.Permission, error) {
	return loadToolPermissions(
		ctx,
		queryer,
		`SELECT tool_name, confirmation_mode, filesystem_scope FROM workspace_tool_permissions WHERE workspace_id = ?`,
		`SELECT tool_name, matcher, target, confirmation_mode FROM workspace_tool_permission_rules WHERE workspace_id = ?`,
		workspaceID,
	)
}

func loadSessionToolPermissions(ctx context.Context, queryer permissionQueryer, sessionID string) ([]toolpolicy.Permission, error) {
	return loadToolPermissions(
		ctx,
		queryer,
		`SELECT tool_name, confirmation_mode, filesystem_scope FROM session_tool_permissions WHERE session_id = ?`,
		`SELECT tool_name, matcher, target, confirmation_mode FROM session_tool_permission_rules WHERE session_id = ?`,
		sessionID,
	)
}

func loadToolPermissions(ctx context.Context, queryer permissionQueryer, permissionQuery, ruleQuery, ownerID string) ([]toolpolicy.Permission, error) {
	permissions := toolpolicy.DefaultPermissions()
	byName := make(map[string]toolpolicy.Permission, len(permissions))
	for _, permission := range permissions {
		byName[permission.ToolName] = permission
	}

	rows, err := queryer.QueryContext(ctx, permissionQuery, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list tool permissions: %w", err)
	}
	for rows.Next() {
		var permission toolpolicy.Permission
		if err := rows.Scan(&permission.ToolName, &permission.ConfirmationMode, &permission.FilesystemScope); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan tool permission: %w", err)
		}
		if _, known := toolpolicy.DefinitionFor(permission.ToolName); !known {
			rows.Close()
			return nil, fmt.Errorf("stored permission references unknown tool %q", permission.ToolName)
		}
		permission.TargetRules = []toolpolicy.TargetRule{}
		byName[permission.ToolName] = permission
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close tool permission rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tool permissions: %w", err)
	}

	ruleRows, err := queryer.QueryContext(ctx, ruleQuery, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list tool permission rules: %w", err)
	}
	for ruleRows.Next() {
		var toolName string
		var rule toolpolicy.TargetRule
		if err := ruleRows.Scan(&toolName, &rule.Matcher, &rule.Target, &rule.ConfirmationMode); err != nil {
			ruleRows.Close()
			return nil, fmt.Errorf("scan tool permission rule: %w", err)
		}
		permission, known := byName[toolName]
		if !known {
			ruleRows.Close()
			return nil, fmt.Errorf("stored rule references unknown tool %q", toolName)
		}
		permission.TargetRules = append(permission.TargetRules, rule)
		byName[toolName] = permission
	}
	if err := ruleRows.Close(); err != nil {
		return nil, fmt.Errorf("close tool permission rule rows: %w", err)
	}
	if err := ruleRows.Err(); err != nil {
		return nil, fmt.Errorf("list tool permission rules: %w", err)
	}

	permissions = permissions[:0]
	for _, definition := range toolpolicy.Definitions() {
		permissions = append(permissions, byName[definition.Name])
	}
	return toolpolicy.NormalizePermissions(permissions)
}

func insertWorkspaceToolPermissions(ctx context.Context, tx *sql.Tx, workspaceID string, permissions []toolpolicy.Permission) error {
	for _, permission := range permissions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_tool_permissions(
			workspace_id, tool_name, confirmation_mode, filesystem_scope
		) VALUES(?, ?, ?, ?)`, workspaceID, permission.ToolName, permission.ConfirmationMode, permission.FilesystemScope); err != nil {
			return fmt.Errorf("insert workspace tool permission: %w", err)
		}
		for _, rule := range permission.TargetRules {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_tool_permission_rules(
				workspace_id, tool_name, matcher, target, confirmation_mode
			) VALUES(?, ?, ?, ?, ?)`, workspaceID, permission.ToolName, rule.Matcher, rule.Target, rule.ConfirmationMode); err != nil {
				return fmt.Errorf("insert workspace tool permission rule: %w", err)
			}
		}
	}
	return nil
}

func insertSessionToolPermissions(ctx context.Context, tx *sql.Tx, sessionID string, permissions []toolpolicy.Permission) error {
	for _, permission := range permissions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_tool_permissions(
			session_id, tool_name, confirmation_mode, filesystem_scope
		) VALUES(?, ?, ?, ?)`, sessionID, permission.ToolName, permission.ConfirmationMode, permission.FilesystemScope); err != nil {
			return fmt.Errorf("insert session tool permission: %w", err)
		}
		for _, rule := range permission.TargetRules {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_tool_permission_rules(
				session_id, tool_name, matcher, target, confirmation_mode
			) VALUES(?, ?, ?, ?, ?)`, sessionID, permission.ToolName, rule.Matcher, rule.Target, rule.ConfirmationMode); err != nil {
				return fmt.Errorf("insert session tool permission rule: %w", err)
			}
		}
	}
	return nil
}

func normalizeToolPermissions(permissions []toolpolicy.Permission) ([]toolpolicy.Permission, error) {
	normalized, err := toolpolicy.NormalizePermissions(permissions)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return normalized, nil
}
