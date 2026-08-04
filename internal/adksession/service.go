package adksession

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// RunnerService returns the ADK session view used by the coordinator runner.
// Isolated child-agent events remain available through Service.Get for the UI
// transcript, but are excluded from ADK's task routing and model history.
func (s *Service) RunnerService() session.Service {
	return &runnerService{Service: s}
}

type runnerService struct {
	*Service
}

func (s *runnerService) Get(
	ctx context.Context,
	req *session.GetRequest,
) (*session.GetResponse, error) {
	response, err := s.Service.Get(ctx, req)
	if err != nil {
		return nil, err
	}
	stored, ok := response.Session.(*persistedSession)
	if !ok {
		return nil, fmt.Errorf("unexpected session type %T", response.Session)
	}
	stored.events = slices.DeleteFunc(stored.events, func(event *session.Event) bool {
		return event != nil && event.IsolationScope != ""
	})
	stored.events = repairInterruptedToolCalls(stored.events)
	return response, nil
}

func repairInterruptedToolCalls(events []*session.Event) []*session.Event {
	for index := 0; index < len(events); index++ {
		event := events[index]
		if event == nil || event.Content == nil {
			continue
		}
		calls := make([]*genai.FunctionCall, 0)
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil {
				calls = append(calls, part.FunctionCall)
			}
		}
		if len(calls) == 0 {
			continue
		}

		if index+1 < len(events) {
			next := events[index+1]
			if next != nil && next.Content != nil && next.Content.Role != genai.RoleModel {
				responses := make(map[string]*genai.Part)
				otherParts := make([]*genai.Part, 0, len(next.Content.Parts))
				for _, part := range next.Content.Parts {
					if part != nil && part.FunctionResponse != nil {
						responses[part.FunctionResponse.ID] = part
						continue
					}
					otherParts = append(otherParts, part)
				}
				missing := false
				orderedResults := make([]*genai.Part, 0, len(calls))
				for _, call := range calls {
					if response := responses[call.ID]; response != nil {
						orderedResults = append(orderedResults, response)
						delete(responses, call.ID)
						continue
					}
					missing = true
					orderedResults = append(orderedResults, interruptedToolResult(call))
				}
				if !missing {
					continue
				}
				for _, part := range next.Content.Parts {
					if part != nil && part.FunctionResponse != nil {
						if _, unmatched := responses[part.FunctionResponse.ID]; unmatched {
							orderedResults = append(orderedResults, part)
							delete(responses, part.FunctionResponse.ID)
						}
					}
				}
				next.Content.Parts = append(orderedResults, otherParts...)
				continue
			}
		}

		parts := make([]*genai.Part, 0, len(calls))
		for _, call := range calls {
			parts = append(parts, interruptedToolResult(call))
		}
		repair := &session.Event{
			ID:           "interrupted-tool-results-" + event.ID,
			Timestamp:    event.Timestamp,
			InvocationID: event.InvocationID,
			Branch:       event.Branch,
			Author:       event.Author,
			LLMResponse: model.LLMResponse{Content: &genai.Content{
				Role:  genai.RoleUser,
				Parts: parts,
			}},
		}
		events = slices.Insert(events, index+1, repair)
		index++
	}
	return events
}

func interruptedToolResult(call *genai.FunctionCall) *genai.Part {
	return &genai.Part{FunctionResponse: &genai.FunctionResponse{
		ID:   call.ID,
		Name: call.Name,
		Response: map[string]any{
			"error": "tool call was interrupted before completion",
		},
	}}
}

type persistedSession struct {
	mu        sync.RWMutex
	appName   string
	userID    string
	id        string
	state     map[string]any
	events    []*session.Event
	updatedAt time.Time
	version   int64
}

func (s *persistedSession) ID() string      { return s.id }
func (s *persistedSession) AppName() string { return s.appName }
func (s *persistedSession) UserID() string  { return s.userID }

func (s *persistedSession) State() session.State {
	return (*stateMap)(s)
}

func (s *persistedSession) Events() session.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return eventList(append([]*session.Event(nil), s.events...))
}

func (s *persistedSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

type stateMap persistedSession

func (s *stateMap) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.state[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *stateMap) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[key] = value
	return nil
}

func (s *stateMap) All() iter.Seq2[string, any] {
	s.mu.RLock()
	copy := maps.Clone(s.state)
	s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for key, value := range copy {
			if !yield(key, value) {
				return
			}
		}
	}
}

type eventList []*session.Event

func (e eventList) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range e {
			if !yield(event) {
				return
			}
		}
	}
}

func (e eventList) Len() int { return len(e) }

func (e eventList) At(index int) *session.Event { return e[index] }

func (s *Service) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req == nil || req.AppName == "" || req.UserID == "" {
		return nil, fmt.Errorf("app_name and user_id are required")
	}
	id := req.SessionID
	if id == "" {
		id = uuid.NewString()
	}
	appDelta, userDelta, sessionDelta := splitState(req.State)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	appState, err := loadState(ctx, tx, `SELECT state_json FROM adk_app_state WHERE app_name = ?`, req.AppName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	userState, err := loadState(ctx, tx, `SELECT state_json FROM adk_user_state WHERE app_name = ? AND user_id = ?`, req.AppName, req.UserID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	maps.Copy(appState, appDelta)
	maps.Copy(userState, userDelta)
	if err := saveScopedStates(ctx, tx, req.AppName, req.UserID, appState, userState); err != nil {
		return nil, err
	}
	stateJSON, err := json.Marshal(sessionDelta)
	if err != nil {
		return nil, fmt.Errorf("encode session state: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO adk_sessions(app_name, user_id, session_id, state_json, updated_at, version) VALUES(?, ?, ?, ?, ?, 0)`, req.AppName, req.UserID, id, string(stateJSON), formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("create ADK session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &session.CreateResponse{Session: &persistedSession{
		appName: req.AppName, userID: req.UserID, id: id,
		state: mergeState(appState, userState, sessionDelta), updatedAt: now,
	}}, nil
}

func (s *Service) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req == nil || req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return nil, fmt.Errorf("app_name, user_id, and session_id are required")
	}
	var stateJSON, updated string
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT state_json, updated_at, version FROM adk_sessions WHERE app_name = ? AND user_id = ? AND session_id = ?`, req.AppName, req.UserID, req.SessionID).Scan(&stateJSON, &updated, &version)
	if err != nil {
		return nil, fmt.Errorf("get ADK session: %w", err)
	}
	sessionState, err := decodeState(stateJSON)
	if err != nil {
		return nil, err
	}
	appState, err := loadState(ctx, s.db, `SELECT state_json FROM adk_app_state WHERE app_name = ?`, req.AppName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	userState, err := loadState(ctx, s.db, `SELECT state_json FROM adk_user_state WHERE app_name = ? AND user_id = ?`, req.AppName, req.UserID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	events, err := s.loadEvents(ctx, req)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return nil, err
	}
	return &session.GetResponse{Session: &persistedSession{
		appName: req.AppName, userID: req.UserID, id: req.SessionID,
		state: mergeState(appState, userState, sessionState), events: events,
		updatedAt: updatedAt, version: version,
	}}, nil
}

func (s *Service) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req == nil || req.AppName == "" {
		return nil, fmt.Errorf("app_name is required")
	}
	query := `SELECT user_id, session_id FROM adk_sessions WHERE app_name = ?`
	args := []any{req.AppName}
	if req.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, req.UserID)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ userID, sessionID string }
	var keys []key
	for rows.Next() {
		var value key
		if err := rows.Scan(&value.userID, &value.sessionID); err != nil {
			return nil, err
		}
		keys = append(keys, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]session.Session, 0, len(keys))
	for _, key := range keys {
		response, err := s.Get(ctx, &session.GetRequest{AppName: req.AppName, UserID: key.userID, SessionID: key.sessionID, NumRecentEvents: 0})
		if err != nil {
			return nil, err
		}
		response.Session.(*persistedSession).events = nil
		result = append(result, response.Session)
	}
	return &session.ListResponse{Sessions: result}, nil
}

func (s *Service) Delete(ctx context.Context, req *session.DeleteRequest) error {
	if req == nil || req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return fmt.Errorf("app_name, user_id, and session_id are required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM adk_sessions WHERE app_name = ? AND user_id = ? AND session_id = ?`, req.AppName, req.UserID, req.SessionID)
	return err
}

func (s *Service) AppendEvent(ctx context.Context, current session.Session, event *session.Event) error {
	if current == nil || event == nil {
		return fmt.Errorf("session and event are required")
	}
	if event.Partial {
		return nil
	}
	stored, ok := current.(*persistedSession)
	if !ok {
		return fmt.Errorf("unexpected session type %T", current)
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	appDelta, userDelta, sessionDelta := splitState(event.Actions.StateDelta)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stateJSON string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT state_json, version FROM adk_sessions WHERE app_name = ? AND user_id = ? AND session_id = ?`, stored.appName, stored.userID, stored.id).Scan(&stateJSON, &version)
	if err != nil {
		return fmt.Errorf("load ADK session for append: %w", err)
	}
	stored.mu.RLock()
	expectedVersion := stored.version
	stored.mu.RUnlock()
	if version != expectedVersion {
		return fmt.Errorf("ADK session changed concurrently: expected version %d, got %d", expectedVersion, version)
	}
	persistentState, err := decodeState(stateJSON)
	if err != nil {
		return err
	}
	maps.Copy(persistentState, sessionDelta)
	appState, err := loadState(ctx, tx, `SELECT state_json FROM adk_app_state WHERE app_name = ?`, stored.appName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	userState, err := loadState(ctx, tx, `SELECT state_json FROM adk_user_state WHERE app_name = ? AND user_id = ?`, stored.appName, stored.userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	maps.Copy(appState, appDelta)
	maps.Copy(userState, userDelta)
	if err := saveScopedStates(ctx, tx, stored.appName, stored.userID, appState, userState); err != nil {
		return err
	}
	encodedState, err := json.Marshal(persistentState)
	if err != nil {
		return err
	}
	encodedEvent, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode ADK event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO adk_events(app_name, user_id, session_id, event_id, invocation_id, event_time, event_json) VALUES(?, ?, ?, ?, ?, ?, ?)`, stored.appName, stored.userID, stored.id, event.ID, event.InvocationID, formatTime(event.Timestamp), string(encodedEvent)); err != nil {
		return fmt.Errorf("append ADK event: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE adk_sessions SET state_json = ?, updated_at = ?, version = version + 1 WHERE app_name = ? AND user_id = ? AND session_id = ? AND version = ?`, string(encodedState), formatTime(event.Timestamp), stored.appName, stored.userID, stored.id, version)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("ADK session changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	stored.mu.Lock()
	stored.events = append(stored.events, event)
	stored.state = mergeState(appState, userState, persistentState)
	stored.updatedAt = event.Timestamp
	stored.version++
	stored.mu.Unlock()
	return nil
}

// AppendTranscriptEvent stores an isolated child-agent event without changing
// the parent session state or optimistic-lock version.
func (s *Service) AppendTranscriptEvent(
	ctx context.Context,
	appName, userID, sessionID string,
	event *session.Event,
) error {
	if appName == "" || userID == "" || sessionID == "" || event == nil {
		return fmt.Errorf("app_name, user_id, session_id, and event are required")
	}
	if event.Partial {
		return nil
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	encodedEvent, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode ADK transcript event: %w", err)
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO adk_events(app_name, user_id, session_id, event_id, invocation_id, event_time, event_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		appName,
		userID,
		sessionID,
		event.ID,
		event.InvocationID,
		formatTime(event.Timestamp),
		string(encodedEvent),
	); err != nil {
		return fmt.Errorf("append ADK transcript event: %w", err)
	}
	return nil
}

func (s *Service) loadEvents(ctx context.Context, req *session.GetRequest) ([]*session.Event, error) {
	query := `SELECT event_json FROM adk_events WHERE app_name = ? AND user_id = ? AND session_id = ?`
	args := []any{req.AppName, req.UserID, req.SessionID}
	if !req.After.IsZero() {
		query += ` AND event_time >= ?`
		args = append(args, formatTime(req.After))
	}
	query += ` ORDER BY sequence DESC`
	if req.NumRecentEvents > 0 {
		query += ` LIMIT ?`
		args = append(args, req.NumRecentEvents)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*session.Event
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event session.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode ADK event: %w", err)
		}
		events = append(events, &event)
	}
	slices.Reverse(events)
	return events, rows.Err()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadState(ctx context.Context, q queryer, query string, args ...any) (map[string]any, error) {
	var raw string
	if err := q.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return map[string]any{}, err
	}
	return decodeState(raw)
}

func decodeState(raw string) (map[string]any, error) {
	result := map[string]any{}
	if raw == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return result, nil
}

func saveScopedStates(ctx context.Context, tx *sql.Tx, appName, userID string, appState, userState map[string]any) error {
	appJSON, err := json.Marshal(appState)
	if err != nil {
		return err
	}
	userJSON, err := json.Marshal(userState)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO adk_app_state(app_name, state_json) VALUES(?, ?) ON CONFLICT(app_name) DO UPDATE SET state_json = excluded.state_json`, appName, string(appJSON)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO adk_user_state(app_name, user_id, state_json) VALUES(?, ?, ?) ON CONFLICT(app_name, user_id) DO UPDATE SET state_json = excluded.state_json`, appName, userID, string(userJSON))
	return err
}

func splitState(values map[string]any) (map[string]any, map[string]any, map[string]any) {
	appState, userState, sessionState := map[string]any{}, map[string]any{}, map[string]any{}
	for key, value := range values {
		switch {
		case strings.HasPrefix(key, session.KeyPrefixApp):
			appState[strings.TrimPrefix(key, session.KeyPrefixApp)] = value
		case strings.HasPrefix(key, session.KeyPrefixUser):
			userState[strings.TrimPrefix(key, session.KeyPrefixUser)] = value
		case strings.HasPrefix(key, session.KeyPrefixTemp):
			continue
		default:
			sessionState[key] = value
		}
	}
	return appState, userState, sessionState
}

func mergeState(appState, userState, sessionState map[string]any) map[string]any {
	result := maps.Clone(sessionState)
	for key, value := range appState {
		result[session.KeyPrefixApp+key] = value
	}
	for key, value := range userState {
		result[session.KeyPrefixUser+key] = value
	}
	return result
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
