package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"materialmind/internal/store"
)

func (e *Engine) ApplyRetention(ctx context.Context) (int, error) {
	settings, err := e.store.GetStorageSettings(ctx)
	if err != nil {
		return 0, err
	}
	if settings.RetentionDays == 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -settings.RetentionDays)
	sessions, err := e.store.ListExpiredSessions(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, session := range sessions {
		if err := e.DeleteSession(ctx, session.ID); err != nil {
			if errors.Is(err, ErrSessionBusy) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return deleted, fmt.Errorf("delete expired session %s: %w", session.ID, err)
		}
		deleted++
	}
	return deleted, nil
}
