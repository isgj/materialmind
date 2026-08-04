package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

func (s *Store) GetSessionNotes(ctx context.Context, sessionID string) (SessionNotes, error) {
	notes, _, err := scanSessionNotes(s.db.QueryRowContext(ctx, `
		SELECT session.id, notes.content, notes.revision, notes.updated_at
		FROM app_sessions AS session
		LEFT JOIN session_notes AS notes ON notes.session_id = session.id
		WHERE session.id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return SessionNotes{}, ErrNotFound
	}
	if err != nil {
		return SessionNotes{}, fmt.Errorf("get session notes: %w", err)
	}
	return notes, nil
}

func (s *Store) UpdateSessionNotes(
	ctx context.Context,
	sessionID, content string,
	expectedRevision int64,
) (SessionNotes, bool, error) {
	if expectedRevision < 0 {
		return SessionNotes{}, false, fmt.Errorf(
			"%w: expected revision must not be negative",
			ErrInvalidInput,
		)
	}
	if !utf8.ValidString(content) {
		return SessionNotes{}, false, fmt.Errorf(
			"%w: session notes must be valid UTF-8",
			ErrInvalidInput,
		)
	}
	if len(content) > 16*1024 {
		return SessionNotes{}, false, fmt.Errorf(
			"%w: session notes must not exceed 16384 bytes",
			ErrInvalidInput,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionNotes{}, false, fmt.Errorf("begin session notes update: %w", err)
	}
	defer tx.Rollback()

	notes, exists, err := scanSessionNotes(tx.QueryRowContext(ctx, `
		SELECT session.id, notes.content, notes.revision, notes.updated_at
		FROM app_sessions AS session
		LEFT JOIN session_notes AS notes ON notes.session_id = session.id
		WHERE session.id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return SessionNotes{}, false, ErrNotFound
	}
	if err != nil {
		return SessionNotes{}, false, fmt.Errorf("load session notes for update: %w", err)
	}
	if notes.Revision != expectedRevision {
		return notes, false, fmt.Errorf(
			"%w: session notes are at revision %d, not %d",
			ErrConflict,
			notes.Revision,
			expectedRevision,
		)
	}
	if notes.Content == content {
		return notes, false, nil
	}

	updatedAt := formatTime(time.Now().UTC())
	nextRevision := notes.Revision + 1
	if !exists {
		_, err = tx.ExecContext(ctx, `INSERT INTO session_notes(
			session_id, content, revision, updated_at
		) VALUES(?, ?, 1, ?)`, sessionID, content, updatedAt)
	} else {
		result, updateErr := tx.ExecContext(ctx, `UPDATE session_notes
			SET content = ?, revision = revision + 1, updated_at = ?
			WHERE session_id = ? AND revision = ?`,
			content, updatedAt, sessionID, expectedRevision)
		if updateErr != nil {
			err = updateErr
		} else if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
			err = affectedErr
		} else if affected != 1 {
			err = fmt.Errorf("%w: session notes changed while updating", ErrConflict)
		}
	}
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return notes, false, err
		}
		return SessionNotes{}, false, fmt.Errorf("update session notes: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionNotes{}, false, fmt.Errorf("commit session notes update: %w", err)
	}
	notes.Content = content
	notes.Revision = nextRevision
	notes.UpdatedAt, _ = parseTime(updatedAt)
	return notes, true, nil
}

func scanSessionNotes(scanner interface{ Scan(...any) error }) (SessionNotes, bool, error) {
	var notes SessionNotes
	var content sql.NullString
	var revision sql.NullInt64
	var updatedAt sql.NullString
	if err := scanner.Scan(&notes.SessionID, &content, &revision, &updatedAt); err != nil {
		return SessionNotes{}, false, err
	}
	if !revision.Valid {
		return notes, false, nil
	}
	notes.Content = content.String
	notes.Revision = revision.Int64
	notes.UpdatedAt, _ = parseTime(updatedAt.String)
	return notes, true, nil
}
