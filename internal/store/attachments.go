package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func prepareRunAttachments(
	runID string,
	attachments []RunAttachment,
	now time.Time,
) ([]RunAttachment, error) {
	result := make([]RunAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Name)
		mimeType := strings.TrimSpace(attachment.MIMEType)
		if name == "" || mimeType == "" {
			return nil, fmt.Errorf("%w: attachment name and MIME type are required", ErrInvalidInput)
		}
		content := append([]byte(nil), attachment.Content...)
		result = append(result, RunAttachment{
			ID:        uuid.NewString(),
			RunID:     runID,
			Name:      name,
			MIMEType:  mimeType,
			Size:      int64(len(content)),
			Content:   content,
			CreatedAt: now,
		})
	}
	return result, nil
}

func insertRunAttachments(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	attachments []RunAttachment,
	now time.Time,
) ([]RunAttachment, error) {
	prepared, err := prepareRunAttachments(runID, attachments, now)
	if err != nil {
		return nil, err
	}
	for _, attachment := range prepared {
		if _, err := tx.ExecContext(
			ctx, `INSERT INTO run_attachments(
			id, run_id, name, mime_type, size, content, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			attachment.ID,
			attachment.RunID,
			attachment.Name,
			attachment.MIMEType,
			attachment.Size,
			attachment.Content,
			formatTime(attachment.CreatedAt),
		); err != nil {
			return nil, fmt.Errorf("insert run attachment: %w", err)
		}
	}
	return prepared, nil
}

func (s *Store) ListRunAttachments(
	ctx context.Context,
	runID string,
) ([]RunAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, name, mime_type, size, created_at
		FROM run_attachments WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run attachments: %w", err)
	}
	defer rows.Close()

	attachments := make([]RunAttachment, 0)
	for rows.Next() {
		attachment, err := scanRunAttachmentMetadata(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list run attachments: %w", err)
	}
	return attachments, nil
}

func (s *Store) GetRunAttachment(ctx context.Context, id string) (RunAttachment, error) {
	var attachment RunAttachment
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, run_id, name, mime_type, size, content, created_at
		FROM run_attachments WHERE id = ?`, id).Scan(
		&attachment.ID,
		&attachment.RunID,
		&attachment.Name,
		&attachment.MIMEType,
		&attachment.Size,
		&attachment.Content,
		&created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunAttachment{}, ErrNotFound
	}
	if err != nil {
		return RunAttachment{}, fmt.Errorf("get run attachment: %w", err)
	}
	attachment.CreatedAt, _ = parseTime(created)
	return attachment, nil
}

func scanRunAttachmentMetadata(
	scanner interface{ Scan(...any) error },
) (RunAttachment, error) {
	var attachment RunAttachment
	var created string
	if err := scanner.Scan(
		&attachment.ID,
		&attachment.RunID,
		&attachment.Name,
		&attachment.MIMEType,
		&attachment.Size,
		&created,
	); err != nil {
		return RunAttachment{}, fmt.Errorf("scan run attachment: %w", err)
	}
	attachment.CreatedAt, _ = parseTime(created)
	return attachment, nil
}
