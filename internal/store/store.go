package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"materialmind/internal/toolpolicy"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrInvalidInput = errors.New("invalid input")
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const defaultContextWindowTokens = 128_000

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	s := &Store{db: db}
	if err := s.initializeSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initializeSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, currentSchema); err != nil {
		return fmt.Errorf("initialize database schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database schema: %w", err)
	}
	return nil
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, root_path, created_at, updated_at FROM workspaces ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	items := make([]Workspace, 0)
	for rows.Next() {
		item, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateWorkspace(ctx context.Context, name, rootPath string) (Workspace, error) {
	name = strings.TrimSpace(name)
	rootPath, err := canonicalDirectory(rootPath)
	if err != nil {
		return Workspace{}, err
	}
	if name == "" {
		name = filepath.Base(rootPath)
	}
	now := time.Now().UTC()
	item := Workspace{ID: uuid.NewString(), Name: name, RootPath: rootPath, Available: true, CreatedAt: now, UpdatedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, fmt.Errorf("begin workspace creation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id, name, root_path, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, item.ID, item.Name, item.RootPath, formatTime(now), formatTime(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Workspace{}, fmt.Errorf("workspace path already exists: %w", ErrConflict)
		}
		return Workspace{}, fmt.Errorf("create workspace: %w", err)
	}
	if err := insertWorkspaceToolPermissions(ctx, tx, item.ID, toolpolicy.DefaultPermissions()); err != nil {
		return Workspace{}, err
	}
	if err := copyDefaultMCPServers(ctx, tx, item.ID); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, fmt.Errorf("commit workspace creation: %w", err)
	}
	return item, nil
}

func (s *Store) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	item, err := scanWorkspace(s.db.QueryRowContext(ctx, `SELECT id, name, root_path, created_at, updated_at FROM workspaces WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateWorkspace(ctx context.Context, id, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?`, name, formatTime(now), id)
	if err != nil {
		return Workspace{}, fmt.Errorf("update workspace: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Workspace{}, ErrNotFound
	}
	return s.GetWorkspace(ctx, id)
}

func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return fmt.Errorf("workspace still has sessions: %w", ErrConflict)
		}
		return fmt.Errorf("delete workspace: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListLLMProviders(ctx context.Context) ([]LLMProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, api_compatibility, base_url, auth_type,
			bearer_token_env_var, created_at, updated_at
			FROM llm_providers ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list llm providers: %w", err)
	}
	defer rows.Close()
	items := make([]LLMProvider, 0)
	for rows.Next() {
		item, err := scanLLMProvider(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateLLMProvider(ctx context.Context, name, apiCompatibility, baseURL, bearerTokenEnvVar string) (LLMProvider, error) {
	authType := LLMAuthNone
	if strings.TrimSpace(bearerTokenEnvVar) != "" {
		authType = LLMAuthBearerEnv
	}
	return s.CreateLLMProviderWithAuth(
		ctx,
		name,
		apiCompatibility,
		baseURL,
		authType,
		bearerTokenEnvVar,
	)
}

func (s *Store) CreateLLMProviderWithAuth(
	ctx context.Context,
	name, apiCompatibility, baseURL, authType, bearerTokenEnvVar string,
) (LLMProvider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return LLMProvider{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	apiCompatibility, err := normalizeAPICompatibility(apiCompatibility)
	if err != nil {
		return LLMProvider{}, err
	}
	baseURL, authType, bearerTokenEnvVar, err = normalizeLLMConnection(
		baseURL,
		authType,
		bearerTokenEnvVar,
	)
	if err != nil {
		return LLMProvider{}, err
	}
	if err := validateLLMProviderConnection(apiCompatibility, baseURL, authType); err != nil {
		return LLMProvider{}, err
	}
	now := time.Now().UTC()
	item := LLMProvider{
		ID: uuid.NewString(), Name: name, APICompatibility: apiCompatibility,
		BaseURL: baseURL, AuthType: authType, BearerTokenEnvVar: bearerTokenEnvVar,
		CredentialAvailable: llmEnvironmentCredentialAvailable(authType, bearerTokenEnvVar),
		CreatedAt:           now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO llm_providers(
			id, name, api_compatibility, base_url, auth_type, bearer_token_env_var, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Name, item.APICompatibility, item.BaseURL,
		item.AuthType, item.BearerTokenEnvVar, formatTime(now), formatTime(now))
	if err != nil {
		return LLMProvider{}, fmt.Errorf("create llm provider: %w", err)
	}
	return item, nil
}

func (s *Store) GetLLMProvider(ctx context.Context, id string) (LLMProvider, error) {
	item, err := scanLLMProvider(s.db.QueryRowContext(ctx, `SELECT id, name, api_compatibility, base_url,
			auth_type, bearer_token_env_var, created_at, updated_at FROM llm_providers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return LLMProvider{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateLLMProvider(ctx context.Context, id, name, apiCompatibility, baseURL, bearerTokenEnvVar string) (LLMProvider, error) {
	authType := LLMAuthNone
	if strings.TrimSpace(bearerTokenEnvVar) != "" {
		authType = LLMAuthBearerEnv
	}
	return s.UpdateLLMProviderWithAuth(
		ctx,
		id,
		name,
		apiCompatibility,
		baseURL,
		authType,
		bearerTokenEnvVar,
	)
}

func (s *Store) UpdateLLMProviderWithAuth(
	ctx context.Context,
	id, name, apiCompatibility, baseURL, authType, bearerTokenEnvVar string,
) (LLMProvider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return LLMProvider{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	apiCompatibility, err := normalizeAPICompatibility(apiCompatibility)
	if err != nil {
		return LLMProvider{}, err
	}
	baseURL, authType, bearerTokenEnvVar, err = normalizeLLMConnection(
		baseURL,
		authType,
		bearerTokenEnvVar,
	)
	if err != nil {
		return LLMProvider{}, err
	}
	if err := validateLLMProviderConnection(apiCompatibility, baseURL, authType); err != nil {
		return LLMProvider{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE llm_providers
			SET name = ?, api_compatibility = ?, base_url = ?, auth_type = ?,
				bearer_token_env_var = ?, updated_at = ?
			WHERE id = ?`, name, apiCompatibility, baseURL, authType, bearerTokenEnvVar,
		formatTime(time.Now()), id)
	if err != nil {
		return LLMProvider{}, fmt.Errorf("update llm provider: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return LLMProvider{}, ErrNotFound
	}
	return s.GetLLMProvider(ctx, id)
}

func (s *Store) DeleteLLMProvider(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM llm_providers WHERE id = ?`, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return fmt.Errorf("provider still has models: %w", ErrConflict)
		}
		return fmt.Errorf("delete llm provider: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListLLMModels(ctx context.Context) ([]LLMModel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, llm_provider_id, name, model_id,
		context_window_tokens, max_output_tokens, reasoning_effort, created_at, updated_at
		FROM llm_models ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	defer rows.Close()
	items := make([]LLMModel, 0)
	for rows.Next() {
		item, err := scanLLMModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateLLMModel(
	ctx context.Context,
	llmProviderID, name, modelID string,
	settings GenerationSettings,
) (LLMModel, error) {
	name, modelID = strings.TrimSpace(name), strings.TrimSpace(modelID)
	if name == "" || modelID == "" {
		return LLMModel{}, fmt.Errorf("%w: name and model ID are required", ErrInvalidInput)
	}
	provider, err := s.GetLLMProvider(ctx, llmProviderID)
	if err != nil {
		return LLMModel{}, err
	}
	settings, err = normalizeGenerationSettings(settings)
	if err != nil {
		return LLMModel{}, err
	}
	if err := validateReasoningEffortForProvider(
		settings.ReasoningEffort,
		provider.APICompatibility,
	); err != nil {
		return LLMModel{}, err
	}
	now := time.Now().UTC()
	item := LLMModel{
		ID: uuid.NewString(), LLMProviderID: llmProviderID, Name: name, ModelID: modelID,
		GenerationSettings: settings, CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO llm_models(
		id, llm_provider_id, name, model_id, context_window_tokens, max_output_tokens,
		reasoning_effort, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.LLMProviderID, item.Name, item.ModelID,
		item.ContextWindowTokens, item.MaxOutputTokens, nullableString(item.ReasoningEffort),
		formatTime(now), formatTime(now))
	if err != nil {
		return LLMModel{}, fmt.Errorf("create llm model: %w", err)
	}
	return item, nil
}

func (s *Store) GetLLMModel(ctx context.Context, id string) (LLMModel, error) {
	item, err := scanLLMModel(s.db.QueryRowContext(ctx, `SELECT id, llm_provider_id, name, model_id,
		context_window_tokens, max_output_tokens, reasoning_effort, created_at, updated_at
		FROM llm_models WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return LLMModel{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateLLMModel(
	ctx context.Context,
	id, llmProviderID, name, modelID string,
	settings GenerationSettings,
) (LLMModel, error) {
	name, modelID = strings.TrimSpace(name), strings.TrimSpace(modelID)
	if name == "" || modelID == "" {
		return LLMModel{}, fmt.Errorf("%w: name and model ID are required", ErrInvalidInput)
	}
	provider, err := s.GetLLMProvider(ctx, llmProviderID)
	if err != nil {
		return LLMModel{}, err
	}
	settings, err = normalizeGenerationSettings(settings)
	if err != nil {
		return LLMModel{}, err
	}
	if err := validateReasoningEffortForProvider(
		settings.ReasoningEffort,
		provider.APICompatibility,
	); err != nil {
		return LLMModel{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE llm_models
		SET llm_provider_id = ?, name = ?, model_id = ?, context_window_tokens = ?,
			max_output_tokens = ?, reasoning_effort = ?, updated_at = ?
		WHERE id = ?`, llmProviderID, name, modelID, settings.ContextWindowTokens,
		settings.MaxOutputTokens, nullableString(settings.ReasoningEffort), formatTime(time.Now()), id)
	if err != nil {
		return LLMModel{}, fmt.Errorf("update llm model: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return LLMModel{}, ErrNotFound
	}
	return s.GetLLMModel(ctx, id)
}

func (s *Store) DeleteLLMModel(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM llm_models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete llm model: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func scanWorkspace(scanner interface{ Scan(...any) error }) (Workspace, error) {
	var item Workspace
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.Name, &item.RootPath, &created, &updated); err != nil {
		return Workspace{}, err
	}
	item.CreatedAt, _ = parseTime(created)
	item.UpdatedAt, _ = parseTime(updated)
	info, err := os.Stat(item.RootPath)
	item.Available = err == nil && info.IsDir()
	return item, nil
}

func scanLLMProvider(scanner interface{ Scan(...any) error }) (LLMProvider, error) {
	var item LLMProvider
	var created, updated string
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.APICompatibility, &item.BaseURL, &item.AuthType,
		&item.BearerTokenEnvVar, &created, &updated,
	); err != nil {
		return LLMProvider{}, err
	}
	item.CredentialAvailable = llmEnvironmentCredentialAvailable(
		item.AuthType,
		item.BearerTokenEnvVar,
	)
	item.CreatedAt, _ = parseTime(created)
	item.UpdatedAt, _ = parseTime(updated)
	return item, nil
}

func scanLLMModel(scanner interface{ Scan(...any) error }) (LLMModel, error) {
	var item LLMModel
	var reasoningEffort sql.NullString
	var created, updated string
	if err := scanner.Scan(
		&item.ID, &item.LLMProviderID, &item.Name, &item.ModelID,
		&item.ContextWindowTokens, &item.MaxOutputTokens,
		&reasoningEffort, &created, &updated,
	); err != nil {
		return LLMModel{}, err
	}
	item.ReasoningEffort = nullableStringPointer(reasoningEffort)
	item.CreatedAt, _ = parseTime(created)
	item.UpdatedAt, _ = parseTime(updated)
	return item, nil
}

func normalizeAPICompatibility(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic", "anthropic-compatible", "claude-compatible":
		return "anthropic", nil
	case "gemini", "google-gemini", "gemini-api":
		return "gemini", nil
	case "openai-chat-completions", "openai-chat", "chat-completions":
		return "openai-chat-completions", nil
	case "openai-responses", "openai-response", "responses":
		return "openai-responses", nil
	default:
		return "", fmt.Errorf("%w: unsupported API compatibility %q", ErrInvalidInput, value)
	}
}

func normalizeGenerationSettings(settings GenerationSettings) (GenerationSettings, error) {
	if settings.MaxOutputTokens < 1 {
		return GenerationSettings{}, fmt.Errorf("%w: max output tokens must be greater than zero", ErrInvalidInput)
	}
	if settings.ContextWindowTokens == 0 {
		settings.ContextWindowTokens = defaultContextWindowTokens
	}
	if settings.ContextWindowTokens < settings.MaxOutputTokens {
		return GenerationSettings{}, fmt.Errorf(
			"%w: context window tokens must be greater than or equal to max output tokens",
			ErrInvalidInput,
		)
	}
	reasoningEffort, err := normalizeReasoningEffort(settings.ReasoningEffort)
	if err != nil {
		return GenerationSettings{}, err
	}
	settings.ReasoningEffort = reasoningEffort
	return settings, nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func normalizeReasoningEffort(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	effort := strings.ToLower(strings.TrimSpace(*value))
	if effort == "" {
		return nil, nil
	}
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return &effort, nil
	default:
		return nil, fmt.Errorf("%w: unsupported reasoning effort %q", ErrInvalidInput, *value)
	}
}

func validateReasoningEffortForProvider(value *string, apiCompatibility string) error {
	if value == nil {
		return nil
	}
	switch apiCompatibility {
	case "anthropic":
		switch *value {
		case "low", "medium", "high", "xhigh", "max":
			return nil
		}
	case "openai-chat-completions", "openai-responses":
		return nil
	}
	return fmt.Errorf(
		"%w: reasoning effort %q is not supported by %s providers",
		ErrInvalidInput,
		*value,
		apiCompatibility,
	)
}

func normalizeLLMConnection(
	baseURL, authType, bearerTokenEnvVar string,
) (string, string, string, error) {
	baseURL = strings.TrimSpace(baseURL)
	authType = strings.ToLower(strings.TrimSpace(authType))
	bearerTokenEnvVar = strings.TrimSpace(bearerTokenEnvVar)
	if authType == "" {
		authType = LLMAuthNone
		if bearerTokenEnvVar != "" {
			authType = LLMAuthBearerEnv
		}
	}
	switch authType {
	case LLMAuthNone, LLMAuthBearerKeyring:
		bearerTokenEnvVar = ""
	case LLMAuthBearerEnv:
		if bearerTokenEnvVar == "" {
			return "", "", "", fmt.Errorf(
				"%w: credential environment variable is required",
				ErrInvalidInput,
			)
		}
		if !environmentName.MatchString(bearerTokenEnvVar) {
			return "", "", "", fmt.Errorf(
				"%w: credential environment variable must be a valid environment variable name",
				ErrInvalidInput,
			)
		}
	default:
		return "", "", "", fmt.Errorf(
			"%w: unsupported LLM authentication type %q",
			ErrInvalidInput,
			authType,
		)
	}
	if baseURL == "" {
		return "", authType, bearerTokenEnvVar, nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", "", "", fmt.Errorf("%w: base URL must be an absolute HTTP or HTTPS URL", ErrInvalidInput)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", "", fmt.Errorf("%w: base URL must use HTTP or HTTPS", ErrInvalidInput)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", fmt.Errorf("%w: base URL cannot contain credentials, a query, or a fragment", ErrInvalidInput)
	}
	return baseURL, authType, bearerTokenEnvVar, nil
}

func validateLLMProviderConnection(apiCompatibility, baseURL, authType string) error {
	if apiCompatibility == "gemini" &&
		baseURL == "" &&
		authType == LLMAuthNone {
		return fmt.Errorf(
			"%w: a credential is required for the default Gemini API endpoint",
			ErrInvalidInput,
		)
	}
	return nil
}

func llmEnvironmentCredentialAvailable(authType, bearerTokenEnvVar string) bool {
	switch authType {
	case LLMAuthNone:
		return true
	case LLMAuthBearerEnv:
		return strings.TrimSpace(os.Getenv(bearerTokenEnvVar)) != ""
	default:
		return false
	}
}

func canonicalDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: root path is required", ErrInvalidInput)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root path: %v", ErrInvalidInput, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root path: %v", ErrInvalidInput, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: inspect root path: %v", ErrInvalidInput, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: root path is not a directory", ErrInvalidInput)
	}
	return filepath.Clean(resolved), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
