package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const maxRetentionDays = 36_500

type StorageSettings struct {
	RetentionDays int `json:"retentionDays"`
}

func (s *Store) GetStorageSettings(ctx context.Context) (StorageSettings, error) {
	var raw string
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT value FROM app_settings WHERE key = 'retention_days'`,
	).Scan(&raw); err != nil {
		return StorageSettings{}, fmt.Errorf("load storage settings: %w", err)
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 || days > maxRetentionDays {
		return StorageSettings{}, fmt.Errorf("load storage settings: invalid retention_days value %q", raw)
	}
	return StorageSettings{RetentionDays: days}, nil
}

func (s *Store) UpdateStorageSettings(
	ctx context.Context,
	settings StorageSettings,
) (StorageSettings, error) {
	if settings.RetentionDays < 0 || settings.RetentionDays > maxRetentionDays {
		return StorageSettings{}, fmt.Errorf(
			"%w: retentionDays must be between 0 and %d",
			ErrInvalidInput,
			maxRetentionDays,
		)
	}
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE app_settings SET value = ?, updated_at = ? WHERE key = 'retention_days'`,
		strconv.Itoa(settings.RetentionDays),
		formatTime(time.Now()),
	)
	if err != nil {
		return StorageSettings{}, fmt.Errorf("update storage settings: %w", err)
	}
	return settings, nil
}

func (s *Store) ListExpiredSessions(
	ctx context.Context,
	olderThan time.Time,
) ([]AppSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, title, runtime_type,
		selected_llm_model_id, acp_agent_id, acp_session_id, acp_config_options_json,
		status, created_at, updated_at
		FROM app_sessions
		WHERE status = 'idle' AND updated_at < ?
		ORDER BY updated_at`, formatTime(olderThan))
	if err != nil {
		return nil, fmt.Errorf("list expired sessions: %w", err)
	}
	defer rows.Close()
	items := make([]AppSession, 0)
	for rows.Next() {
		item, err := scanAppSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan expired session: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Backup writes a consistent SQLite snapshot to a destination that must not exist.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("create database backup: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("secure database backup: %w", err)
	}
	return nil
}
