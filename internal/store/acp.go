package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxACPArguments     = 128
	maxACPArgumentRunes = 16_384
)

func (s *Store) ListACPAgents(ctx context.Context) ([]ACPAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, command, arguments_json, created_at, updated_at
		FROM acp_agents ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list ACP agents: %w", err)
	}
	defer rows.Close()

	items := make([]ACPAgent, 0)
	for rows.Next() {
		item, err := scanACPAgent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateACPAgent(ctx context.Context, name, command string, arguments []string) (ACPAgent, error) {
	name, command, arguments, err := normalizeACPAgent(name, command, arguments)
	if err != nil {
		return ACPAgent{}, err
	}
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		return ACPAgent{}, fmt.Errorf("encode ACP agent arguments: %w", err)
	}
	now := time.Now().UTC()
	item := ACPAgent{
		ID:        uuid.NewString(),
		Name:      name,
		Command:   command,
		Arguments: arguments,
		CreatedAt: now,
		UpdatedAt: now,
	}
	setACPAgentAvailability(&item)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO acp_agents(
		id, name, command, arguments_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?)`, item.ID, item.Name, item.Command, string(encodedArguments),
		formatTime(now), formatTime(now)); err != nil {
		return ACPAgent{}, fmt.Errorf("create ACP agent: %w", err)
	}
	return item, nil
}

func (s *Store) GetACPAgent(ctx context.Context, id string) (ACPAgent, error) {
	item, err := scanACPAgent(s.db.QueryRowContext(ctx, `SELECT id, name, command, arguments_json,
		created_at, updated_at FROM acp_agents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ACPAgent{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateACPAgent(ctx context.Context, id, name, command string, arguments []string) (ACPAgent, error) {
	name, command, arguments, err := normalizeACPAgent(name, command, arguments)
	if err != nil {
		return ACPAgent{}, err
	}
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		return ACPAgent{}, fmt.Errorf("encode ACP agent arguments: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE acp_agents
		SET name = ?, command = ?, arguments_json = ?, updated_at = ?
		WHERE id = ?`, name, command, string(encodedArguments), formatTime(time.Now()), id)
	if err != nil {
		return ACPAgent{}, fmt.Errorf("update ACP agent: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ACPAgent{}, ErrNotFound
	}
	return s.GetACPAgent(ctx, id)
}

func (s *Store) DeleteACPAgent(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM acp_agents WHERE id = ?`, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return fmt.Errorf("ACP agent is used by a session: %w", ErrConflict)
		}
		return fmt.Errorf("delete ACP agent: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateACPSessionConnection(
	ctx context.Context,
	sessionID, acpSessionID string,
	configOptions json.RawMessage,
) (AppSession, error) {
	if len(configOptions) == 0 {
		configOptions = json.RawMessage("[]")
	}
	if !json.Valid(configOptions) {
		return AppSession{}, fmt.Errorf("%w: ACP configuration options are invalid JSON", ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_sessions
		SET acp_session_id = ?, acp_config_options_json = ?, updated_at = ?
		WHERE id = ? AND runtime_type = 'acp'`, acpSessionID, string(configOptions),
		formatTime(time.Now()), sessionID)
	if err != nil {
		return AppSession{}, fmt.Errorf("update ACP session connection: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return AppSession{}, ErrNotFound
	}
	return s.GetSession(ctx, sessionID)
}

func (s *Store) UpdateACPSessionConfigOptions(
	ctx context.Context,
	sessionID string,
	configOptions json.RawMessage,
) (AppSession, error) {
	sessionRecord, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return AppSession{}, err
	}
	return s.UpdateACPSessionConnection(ctx, sessionID, sessionRecord.ACPSessionID, configOptions)
}

func (s *Store) UpsertACPTranscriptItem(
	ctx context.Context,
	sessionID string,
	item TranscriptItem,
) (TranscriptItem, error) {
	if item.ID == "" || item.Kind == "" {
		return TranscriptItem{}, fmt.Errorf("%w: transcript item ID and kind are required", ErrInvalidInput)
	}
	inputJSON, err := json.Marshal(emptyMap(item.ToolInput))
	if err != nil {
		return TranscriptItem{}, fmt.Errorf("encode ACP tool input: %w", err)
	}
	outputJSON, err := json.Marshal(emptyMap(item.ToolOutput))
	if err != nil {
		return TranscriptItem{}, fmt.Errorf("encode ACP tool output: %w", err)
	}
	if item.Provider == "" {
		item.Provider = RuntimeACP
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO acp_transcript_items(
		id, session_id, invocation_id, kind, role, text, tool_name, tool_call_id,
		tool_input_json, tool_output_json, provider, model, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		invocation_id = excluded.invocation_id,
		kind = excluded.kind,
		role = excluded.role,
		text = excluded.text,
		tool_name = excluded.tool_name,
		tool_call_id = excluded.tool_call_id,
		tool_input_json = excluded.tool_input_json,
		tool_output_json = excluded.tool_output_json,
		provider = excluded.provider,
		model = excluded.model,
		updated_at = excluded.updated_at`,
		item.ID, sessionID, item.InvocationID, item.Kind, item.Role, item.Text, item.ToolName,
		item.ToolCallID, string(inputJSON), string(outputJSON), item.Provider, item.Model,
		formatTime(item.CreatedAt), formatTime(now))
	if err != nil {
		return TranscriptItem{}, fmt.Errorf("save ACP transcript item: %w", err)
	}
	return item, nil
}

func (s *Store) ListACPTranscript(ctx context.Context, sessionID string) ([]TranscriptItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, invocation_id, kind, role, text, tool_name,
		tool_call_id, tool_input_json, tool_output_json, provider, model, created_at
		FROM acp_transcript_items WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list ACP transcript: %w", err)
	}
	defer rows.Close()

	items := make([]TranscriptItem, 0)
	for rows.Next() {
		var item TranscriptItem
		var inputJSON, outputJSON, created string
		if err := rows.Scan(
			&item.ID, &item.InvocationID, &item.Kind, &item.Role, &item.Text, &item.ToolName,
			&item.ToolCallID, &inputJSON, &outputJSON, &item.Provider, &item.Model, &created,
		); err != nil {
			return nil, fmt.Errorf("scan ACP transcript item: %w", err)
		}
		if err := json.Unmarshal([]byte(inputJSON), &item.ToolInput); err != nil {
			return nil, fmt.Errorf("decode ACP tool input: %w", err)
		}
		if err := json.Unmarshal([]byte(outputJSON), &item.ToolOutput); err != nil {
			return nil, fmt.Errorf("decode ACP tool output: %w", err)
		}
		item.CreatedAt, _ = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanACPAgent(scanner interface{ Scan(...any) error }) (ACPAgent, error) {
	var item ACPAgent
	var argumentsJSON, created, updated string
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.Command, &argumentsJSON, &created, &updated,
	); err != nil {
		return ACPAgent{}, err
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &item.Arguments); err != nil {
		return ACPAgent{}, fmt.Errorf("decode ACP agent arguments: %w", err)
	}
	if item.Arguments == nil {
		item.Arguments = make([]string, 0)
	}
	item.CreatedAt, _ = parseTime(created)
	item.UpdatedAt, _ = parseTime(updated)
	setACPAgentAvailability(&item)
	return item, nil
}

func normalizeACPAgent(name, command string, arguments []string) (string, string, []string, error) {
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" || command == "" {
		return "", "", nil, fmt.Errorf("%w: name and command are required", ErrInvalidInput)
	}
	if strings.IndexByte(command, 0) >= 0 {
		return "", "", nil, fmt.Errorf("%w: command cannot contain a NUL byte", ErrInvalidInput)
	}
	if len(arguments) > maxACPArguments {
		return "", "", nil, fmt.Errorf("%w: ACP agents support at most %d arguments", ErrInvalidInput, maxACPArguments)
	}
	normalized := make([]string, len(arguments))
	for index, argument := range arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return "", "", nil, fmt.Errorf("%w: argument %d cannot contain a NUL byte", ErrInvalidInput, index+1)
		}
		if utf8.RuneCountInString(argument) > maxACPArgumentRunes {
			return "", "", nil, fmt.Errorf("%w: argument %d is too long", ErrInvalidInput, index+1)
		}
		normalized[index] = argument
	}
	return name, command, normalized, nil
}

func setACPAgentAvailability(item *ACPAgent) {
	resolved, err := exec.LookPath(item.Command)
	item.Available = err == nil
	if err == nil {
		item.ResolvedCommand = resolved
	}
}

func emptyMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
