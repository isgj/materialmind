package engine

import (
	"context"
	"fmt"

	"materialmind/internal/store"
)

type TranscriptPage struct {
	Items      []store.TranscriptItem `json:"items"`
	NextCursor *int                   `json:"nextCursor,omitempty"`
	HasMore    bool                   `json:"hasMore"`
}

func (e *Engine) TranscriptPage(
	ctx context.Context,
	sessionID string,
	before *int,
	limit int,
) (TranscriptPage, error) {
	if limit < 1 {
		return TranscriptPage{}, fmt.Errorf("%w: transcript page limit must be positive", store.ErrInvalidInput)
	}
	if before != nil && *before < 0 {
		return TranscriptPage{}, fmt.Errorf("%w: transcript cursor must not be negative", store.ErrInvalidInput)
	}
	items, err := e.Transcript(ctx, sessionID)
	if err != nil {
		return TranscriptPage{}, err
	}
	return paginateTranscript(items, before, limit), nil
}

func paginateTranscript(
	items []store.TranscriptItem,
	before *int,
	limit int,
) TranscriptPage {
	end := len(items)
	if before != nil {
		end = min(max(*before, 0), end)
	}
	start := max(0, end-limit)
	start = expandTranscriptPageForDelegations(items, start, end)
	pageItems := append(make([]store.TranscriptItem, 0, end-start), items[start:end]...)
	page := TranscriptPage{Items: pageItems, HasMore: start > 0}
	if page.HasMore {
		page.NextCursor = &start
	}
	return page
}

type transcriptDelegationKey struct {
	invocationID string
	toolCallID   string
}

func expandTranscriptPageForDelegations(items []store.TranscriptItem, start, end int) int {
	parentIndices := make(map[transcriptDelegationKey]int)
	for index, item := range items[:end] {
		if item.Kind == "subagent_call" && item.ToolCallID != "" {
			parentIndices[transcriptDelegationKey{
				invocationID: item.InvocationID,
				toolCallID:   item.ToolCallID,
			}] = index
		}
	}

	for {
		adjustedStart := start
		for _, item := range items[start:end] {
			key, ok := transcriptItemDelegationKey(item)
			if !ok {
				continue
			}
			if parentIndex, found := parentIndices[key]; found {
				adjustedStart = min(adjustedStart, parentIndex)
			}
		}
		if adjustedStart == start {
			return start
		}
		start = adjustedStart
	}
}

func transcriptItemDelegationKey(item store.TranscriptItem) (transcriptDelegationKey, bool) {
	toolCallID := item.DelegationID
	if item.Kind == "subagent_call" || item.Kind == "subagent_result" {
		toolCallID = item.ToolCallID
	}
	if toolCallID == "" {
		return transcriptDelegationKey{}, false
	}
	return transcriptDelegationKey{
		invocationID: item.InvocationID,
		toolCallID:   toolCallID,
	}, true
}
