package engine

import (
	"testing"

	"materialmind/internal/store"
)

func TestPaginateTranscriptFromNewestToOldest(t *testing.T) {
	items := []store.TranscriptItem{
		{ID: "one"},
		{ID: "two"},
		{ID: "three"},
		{ID: "four"},
		{ID: "five"},
	}

	latest := paginateTranscript(items, nil, 2)
	if !latest.HasMore || latest.NextCursor == nil || *latest.NextCursor != 3 ||
		latest.Items[0].ID != "four" || latest.Items[1].ID != "five" {
		t.Fatalf("latest page = %#v", latest)
	}
	older := paginateTranscript(items, latest.NextCursor, 2)
	if !older.HasMore || older.NextCursor == nil || *older.NextCursor != 1 ||
		older.Items[0].ID != "two" || older.Items[1].ID != "three" {
		t.Fatalf("older page = %#v", older)
	}
	oldest := paginateTranscript(items, older.NextCursor, 2)
	if oldest.HasMore || oldest.NextCursor != nil || len(oldest.Items) != 1 || oldest.Items[0].ID != "one" {
		t.Fatalf("oldest page = %#v", oldest)
	}
}

func TestPaginateEmptyTranscriptReturnsArray(t *testing.T) {
	page := paginateTranscript(nil, nil, 100)
	if page.Items == nil || len(page.Items) != 0 || page.HasMore || page.NextCursor != nil {
		t.Fatalf("empty page = %#v", page)
	}
}

func TestPaginateTranscriptKeepsDelegatedWorkWithItsParent(t *testing.T) {
	const (
		invocationID = "invocation-1"
		delegationID = "delegation-1"
	)
	items := []store.TranscriptItem{
		{ID: "older-one"},
		{ID: "older-two"},
		{
			ID:           "delegation-call",
			InvocationID: invocationID,
			Kind:         "subagent_call",
			ToolCallID:   delegationID,
		},
	}
	for range 179 {
		items = append(items, store.TranscriptItem{
			InvocationID: invocationID,
			Kind:         "tool_call",
			DelegationID: delegationID,
		})
	}
	items = append(items, store.TranscriptItem{
		ID:           "delegation-result",
		InvocationID: invocationID,
		Kind:         "subagent_result",
		ToolCallID:   delegationID,
	})

	latest := paginateTranscript(items, nil, 100)
	if !latest.HasMore || latest.NextCursor == nil || *latest.NextCursor != 2 {
		t.Fatalf("latest page cursor = %#v", latest.NextCursor)
	}
	if len(latest.Items) != 181 || latest.Items[0].ID != "delegation-call" ||
		latest.Items[len(latest.Items)-1].ID != "delegation-result" {
		t.Fatalf("latest page delegation bounds = %#v ... %#v, count = %d", latest.Items[0], latest.Items[len(latest.Items)-1], len(latest.Items))
	}

	oldest := paginateTranscript(items, latest.NextCursor, 100)
	if oldest.HasMore || oldest.NextCursor != nil || len(oldest.Items) != 2 ||
		oldest.Items[0].ID != "older-one" || oldest.Items[1].ID != "older-two" {
		t.Fatalf("oldest page = %#v", oldest)
	}
}
