package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *Store) ListSessions(ctx context.Context, workspaceID string) ([]AppSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, title, runtime_type,
		selected_llm_model_id, acp_agent_id, acp_session_id, acp_config_options_json,
		status, created_at, updated_at
		FROM app_sessions WHERE workspace_id = ? ORDER BY updated_at DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	items := make([]AppSession, 0)
	for rows.Next() {
		item, err := scanAppSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListAllSessions(ctx context.Context) ([]AppSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, title, runtime_type,
		selected_llm_model_id, acp_agent_id, acp_session_id, acp_config_options_json,
		status, created_at, updated_at
		FROM app_sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	items := make([]AppSession, 0)
	for rows.Next() {
		item, err := scanAppSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateSession(ctx context.Context, workspaceID, title string, llmModelID *string) (AppSession, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return AppSession{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New session"
	}
	if llmModelID != nil {
		if _, err := s.GetLLMModel(ctx, *llmModelID); err != nil {
			return AppSession{}, err
		}
	}
	now := time.Now().UTC()
	item := AppSession{
		ID:                 uuid.NewString(),
		WorkspaceID:        workspaceID,
		Title:              title,
		RuntimeType:        RuntimeADK,
		SelectedLLMModelID: llmModelID,
		ACPConfigOptions:   json.RawMessage("[]"),
		Status:             "idle",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AppSession{}, fmt.Errorf("begin session creation: %w", err)
	}
	defer tx.Rollback()
	permissions, err := loadWorkspaceToolPermissions(ctx, tx, workspaceID)
	if err != nil {
		return AppSession{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_sessions(id, workspace_id, title, selected_llm_model_id, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, item.ID, item.WorkspaceID, item.Title, nullableString(item.SelectedLLMModelID), item.Status, formatTime(now), formatTime(now))
	if err != nil {
		return AppSession{}, fmt.Errorf("create session: %w", err)
	}
	if err := insertSessionToolPermissions(ctx, tx, item.ID, permissions); err != nil {
		return AppSession{}, err
	}
	if err := copyWorkspaceMCPServers(ctx, tx, workspaceID, item.ID); err != nil {
		return AppSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppSession{}, fmt.Errorf("commit session creation: %w", err)
	}
	return item, nil
}

func (s *Store) CreateACPSession(ctx context.Context, workspaceID, title, acpAgentID string) (AppSession, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return AppSession{}, err
	}
	if _, err := s.GetACPAgent(ctx, acpAgentID); err != nil {
		return AppSession{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New session"
	}
	now := time.Now().UTC()
	item := AppSession{
		ID:               uuid.NewString(),
		WorkspaceID:      workspaceID,
		Title:            title,
		RuntimeType:      RuntimeACP,
		ACPAgentID:       &acpAgentID,
		ACPConfigOptions: json.RawMessage("[]"),
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AppSession{}, fmt.Errorf("begin ACP session creation: %w", err)
	}
	defer tx.Rollback()
	permissions, err := loadWorkspaceToolPermissions(ctx, tx, workspaceID)
	if err != nil {
		return AppSession{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_sessions(
		id, workspace_id, title, runtime_type, selected_llm_model_id, acp_agent_id,
		acp_session_id, acp_config_options_json, status, created_at, updated_at
	) VALUES(?, ?, ?, 'acp', NULL, ?, '', '[]', ?, ?, ?)`,
		item.ID, item.WorkspaceID, item.Title, acpAgentID, item.Status, formatTime(now), formatTime(now))
	if err != nil {
		return AppSession{}, fmt.Errorf("create ACP session: %w", err)
	}
	if err := insertSessionToolPermissions(ctx, tx, item.ID, permissions); err != nil {
		return AppSession{}, err
	}
	if err := copyWorkspaceMCPServers(ctx, tx, workspaceID, item.ID); err != nil {
		return AppSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppSession{}, fmt.Errorf("commit ACP session creation: %w", err)
	}
	return item, nil
}

func (s *Store) GetSession(ctx context.Context, id string) (AppSession, error) {
	item, err := scanAppSession(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, title,
		runtime_type, selected_llm_model_id, acp_agent_id, acp_session_id,
		acp_config_options_json, status, created_at, updated_at
		FROM app_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AppSession{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateSession(ctx context.Context, id, title string, llmModelID *string) (AppSession, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return AppSession{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	sessionRecord, err := s.GetSession(ctx, id)
	if err != nil {
		return AppSession{}, err
	}
	if sessionRecord.RuntimeType == RuntimeACP {
		llmModelID = nil
	} else if llmModelID != nil {
		if _, err := s.GetLLMModel(ctx, *llmModelID); err != nil {
			return AppSession{}, err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_sessions
		SET title = ?, selected_llm_model_id = ?, updated_at = ? WHERE id = ?`,
		title, nullableString(llmModelID), formatTime(time.Now()), id)
	if err != nil {
		return AppSession{}, fmt.Errorf("update session: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return AppSession{}, ErrNotFound
	}
	return s.GetSession(ctx, id)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateRun(
	ctx context.Context,
	sessionID, llmModelID, message string,
	overrides RunGenerationOverrides,
) (Run, error) {
	return s.CreateRunWithAttachments(
		ctx,
		sessionID,
		llmModelID,
		message,
		overrides,
		nil,
	)
}

func (s *Store) CreateRunWithAttachments(
	ctx context.Context,
	sessionID, llmModelID, message string,
	overrides RunGenerationOverrides,
	attachments []RunAttachment,
) (Run, error) {
	model, err := s.GetLLMModel(ctx, llmModelID)
	if err != nil {
		return Run{}, err
	}
	provider, err := s.GetLLMProvider(ctx, model.LLMProviderID)
	if err != nil {
		return Run{}, err
	}
	sessionRecord, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Run{}, err
	}
	if sessionRecord.RuntimeType != RuntimeADK {
		return Run{}, fmt.Errorf("%w: session does not use the MaterialMind ADK runtime", ErrInvalidInput)
	}
	generationSettings := model.GenerationSettings
	if err := validateReasoningEffortForProvider(
		generationSettings.ReasoningEffort,
		provider.APICompatibility,
	); err != nil {
		return Run{}, err
	}
	if overrides.ReasoningEffort != nil {
		reasoningEffort, err := normalizeReasoningEffort(overrides.ReasoningEffort)
		if err != nil {
			return Run{}, err
		}
		if err := validateReasoningEffortForProvider(
			reasoningEffort,
			provider.APICompatibility,
		); err != nil {
			return Run{}, err
		}
		generationSettings.ReasoningEffort = reasoningEffort
	}
	now := time.Now().UTC()
	run := Run{
		ID:                 uuid.NewString(),
		SessionID:          sessionID,
		Status:             "queued",
		RuntimeType:        RuntimeADK,
		LLMProviderID:      provider.ID,
		LLMProviderName:    provider.Name,
		LLMModelID:         model.ID,
		LLMModelName:       model.Name,
		APICompatibility:   provider.APICompatibility,
		ModelID:            model.ModelID,
		GenerationSettings: generationSettings,
		BaseURL:            provider.BaseURL,
		BearerTokenEnvVar:  provider.BearerTokenEnvVar,
		UserMessage:        message,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin run: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(
		id, session_id, status, llm_configuration_id, llm_configuration_name, llm_provider_id,
		llm_provider_name, provider, model, context_window_tokens, max_output_tokens,
		reasoning_effort, base_url, bearer_token_env_var, user_message, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SessionID, run.Status,
		run.LLMModelID, run.LLMModelName, run.LLMProviderID, run.LLMProviderName, run.APICompatibility,
		run.ModelID, run.ContextWindowTokens, run.MaxOutputTokens, nullableString(run.ReasoningEffort),
		run.BaseURL, run.BearerTokenEnvVar, run.UserMessage,
		formatTime(now), formatTime(now)); err != nil {
		return Run{}, fmt.Errorf("insert run: %w", err)
	}
	run.Attachments, err = insertRunAttachments(ctx, tx, run.ID, attachments, now)
	if err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_sessions SET selected_llm_model_id = ?, status = 'queued', updated_at = ? WHERE id = ?`, model.ID, formatTime(now), sessionID); err != nil {
		return Run{}, fmt.Errorf("update session for run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit run: %w", err)
	}
	return run, nil
}

func (s *Store) CreateACPRun(ctx context.Context, sessionID, message string) (Run, error) {
	return s.CreateACPRunWithAttachments(ctx, sessionID, message, nil)
}

func (s *Store) CreateACPRunWithAttachments(
	ctx context.Context,
	sessionID, message string,
	attachments []RunAttachment,
) (Run, error) {
	sessionRecord, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Run{}, err
	}
	if sessionRecord.RuntimeType != RuntimeACP || sessionRecord.ACPAgentID == nil {
		return Run{}, fmt.Errorf("%w: session does not use an ACP agent runtime", ErrInvalidInput)
	}
	agentRecord, err := s.GetACPAgent(ctx, *sessionRecord.ACPAgentID)
	if err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	run := Run{
		ID:               uuid.NewString(),
		SessionID:        sessionID,
		Status:           "queued",
		RuntimeType:      RuntimeACP,
		ACPAgentID:       agentRecord.ID,
		ACPAgentName:     agentRecord.Name,
		APICompatibility: RuntimeACP,
		ModelID:          agentRecord.Name,
		LLMModelName:     agentRecord.Name,
		GenerationSettings: GenerationSettings{
			ContextWindowTokens: 1,
			MaxOutputTokens:     1,
		},
		UserMessage: message,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run.InvocationID = run.ID

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin ACP run: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(
		id, session_id, invocation_id, status, runtime_type, acp_agent_id, acp_agent_name,
		llm_configuration_id, llm_configuration_name, llm_provider_id, llm_provider_name,
		provider, model, context_window_tokens, max_output_tokens,
		user_message, created_at, updated_at
	) VALUES(?, ?, ?, ?, 'acp', ?, ?, '', ?, '', '', 'acp', ?, 1, 1, ?, ?, ?)`,
		run.ID, run.SessionID, run.InvocationID, run.Status, run.ACPAgentID, run.ACPAgentName,
		run.LLMModelName, run.ModelID, run.UserMessage, formatTime(now), formatTime(now)); err != nil {
		return Run{}, fmt.Errorf("insert ACP run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO acp_transcript_items(
		id, session_id, invocation_id, kind, role, text, provider, model, created_at, updated_at
	) VALUES(?, ?, ?, 'message', 'user', ?, 'acp', ?, ?, ?)`,
		run.ID+":user", run.SessionID, run.InvocationID, run.UserMessage, run.ModelID,
		formatTime(now), formatTime(now)); err != nil {
		return Run{}, fmt.Errorf("insert ACP user message: %w", err)
	}
	run.Attachments, err = insertRunAttachments(ctx, tx, run.ID, attachments, now)
	if err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_sessions
		SET status = 'queued', updated_at = ? WHERE id = ?`, formatTime(now), sessionID); err != nil {
		return Run{}, fmt.Errorf("update session for ACP run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit ACP run: %w", err)
	}
	return run, nil
}

func (s *Store) UpdateRun(ctx context.Context, id, status, invocationID, errorMessage string) (Run, error) {
	now := time.Now().UTC()
	var completed any
	if status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted" {
		completed = formatTime(now)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, invocation_id = CASE WHEN ? = '' THEN invocation_id ELSE ? END,
		error = ?, updated_at = ?, completed_at = ? WHERE id = ?`, status, invocationID, invocationID, errorMessage, formatTime(now), completed, id)
	if err != nil {
		return Run{}, fmt.Errorf("update run: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Run{}, ErrNotFound
	}
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM runs WHERE id = ?`, id).Scan(&sessionID); err != nil {
		return Run{}, err
	}
	sessionStatus := "running"
	if completed != nil {
		sessionStatus = "idle"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_sessions SET status = ?, updated_at = ? WHERE id = ?`, sessionStatus, formatTime(now), sessionID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, id)
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx, `SELECT id, session_id, invocation_id, status,
		runtime_type, acp_agent_id, acp_agent_name,
		llm_provider_id, llm_provider_name, llm_configuration_id, llm_configuration_name,
		provider, model, context_window_tokens, max_output_tokens,
		reasoning_effort,
		base_url, bearer_token_env_var, user_message, error,
		created_at, updated_at, completed_at FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	run.Attachments, err = s.ListRunAttachments(ctx, run.ID)
	return run, err
}

func (s *Store) ListRuns(ctx context.Context, sessionID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, invocation_id, status,
		runtime_type, acp_agent_id, acp_agent_name,
		llm_provider_id, llm_provider_name, llm_configuration_id, llm_configuration_name,
		provider, model, context_window_tokens, max_output_tokens,
		reasoning_effort,
		base_url, bearer_token_env_var, user_message, error,
		created_at, updated_at, completed_at
		FROM runs WHERE session_id = ? ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range runs {
		attachments, err := s.ListRunAttachments(ctx, runs[index].ID)
		if err != nil {
			return nil, err
		}
		runs[index].Attachments = attachments
	}
	return runs, nil
}

func (s *Store) InterruptRunningRuns(ctx context.Context) error {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'interrupted', error = 'Backend restarted', updated_at = ?, completed_at = ? WHERE status IN ('queued', 'running')`, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_sessions SET status = 'idle', updated_at = ? WHERE status IN ('queued', 'running')`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func scanAppSession(scanner interface{ Scan(...any) error }) (AppSession, error) {
	var item AppSession
	var selected, acpAgentID sql.NullString
	var configOptions string
	var created, updated string
	if err := scanner.Scan(
		&item.ID, &item.WorkspaceID, &item.Title, &item.RuntimeType, &selected, &acpAgentID,
		&item.ACPSessionID, &configOptions, &item.Status, &created, &updated,
	); err != nil {
		return AppSession{}, err
	}
	if selected.Valid {
		item.SelectedLLMModelID = &selected.String
	}
	if acpAgentID.Valid {
		item.ACPAgentID = &acpAgentID.String
	}
	item.ACPConfigOptions = json.RawMessage(configOptions)
	if len(item.ACPConfigOptions) == 0 || !json.Valid(item.ACPConfigOptions) {
		item.ACPConfigOptions = json.RawMessage("[]")
	}
	item.CreatedAt, _ = parseTime(created)
	item.UpdatedAt, _ = parseTime(updated)
	return item, nil
}

func scanRun(scanner interface{ Scan(...any) error }) (Run, error) {
	var run Run
	var reasoningEffort sql.NullString
	var created, updated string
	var completed sql.NullString
	if err := scanner.Scan(&run.ID, &run.SessionID, &run.InvocationID, &run.Status,
		&run.RuntimeType, &run.ACPAgentID, &run.ACPAgentName,
		&run.LLMProviderID, &run.LLMProviderName, &run.LLMModelID, &run.LLMModelName,
		&run.APICompatibility, &run.ModelID, &run.ContextWindowTokens, &run.MaxOutputTokens,
		&reasoningEffort, &run.BaseURL, &run.BearerTokenEnvVar,
		&run.UserMessage, &run.Error, &created, &updated, &completed); err != nil {
		return Run{}, err
	}
	run.ReasoningEffort = nullableStringPointer(reasoningEffort)
	run.CreatedAt, _ = parseTime(created)
	run.UpdatedAt, _ = parseTime(updated)
	if completed.Valid {
		value, _ := parseTime(completed.String)
		run.CompletedAt = &value
	}
	return run, nil
}

func nullableString(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}
