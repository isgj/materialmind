package llmcredentials

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"materialmind/internal/credentialstore"
	"materialmind/internal/store"
)

func Resolve(credentials credentialstore.Store, provider store.LLMProvider) (string, error) {
	switch provider.AuthType {
	case store.LLMAuthNone:
		return "", nil
	case store.LLMAuthBearerEnv:
		token := strings.TrimSpace(os.Getenv(provider.BearerTokenEnvVar))
		if token == "" {
			return "", fmt.Errorf(
				"%w: credential environment variable %q is not set",
				store.ErrInvalidInput,
				provider.BearerTokenEnvVar,
			)
		}
		return token, nil
	case store.LLMAuthBearerKeyring:
		if credentials == nil {
			return "", fmt.Errorf("%w: credential store is unavailable", store.ErrInvalidInput)
		}
		token, err := credentials.Get(credentialstore.LLMProviderTokenKey(provider.ID))
		if errors.Is(err, credentialstore.ErrNotFound) {
			return "", fmt.Errorf(
				"%w: no credential is stored for LLM provider %q",
				store.ErrInvalidInput,
				provider.Name,
			)
		}
		if err != nil {
			return "", fmt.Errorf("read LLM provider credential: %w", err)
		}
		if strings.TrimSpace(token) == "" {
			return "", fmt.Errorf(
				"%w: no credential is stored for LLM provider %q",
				store.ErrInvalidInput,
				provider.Name,
			)
		}
		return strings.TrimSpace(token), nil
	default:
		return "", fmt.Errorf(
			"%w: unsupported LLM authentication type %q",
			store.ErrInvalidInput,
			provider.AuthType,
		)
	}
}

func Available(
	credentials credentialstore.Store,
	provider store.LLMProvider,
) (available bool, backend string, err error) {
	switch provider.AuthType {
	case store.LLMAuthNone:
		return true, "", nil
	case store.LLMAuthBearerEnv:
		return strings.TrimSpace(os.Getenv(provider.BearerTokenEnvVar)) != "", "environment", nil
	case store.LLMAuthBearerKeyring:
		if credentials == nil {
			return false, "", nil
		}
		token, getErr := credentials.Get(credentialstore.LLMProviderTokenKey(provider.ID))
		backend = credentials.Backend()
		if errors.Is(getErr, credentialstore.ErrNotFound) {
			return false, backend, nil
		}
		if getErr != nil {
			return false, backend, getErr
		}
		return strings.TrimSpace(token) != "", backend, nil
	default:
		return false, "", fmt.Errorf("unsupported LLM authentication type %q", provider.AuthType)
	}
}

func Set(credentials credentialstore.Store, providerID, token string) error {
	if credentials == nil {
		return fmt.Errorf("credential store is unavailable")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("%w: credential is required", store.ErrInvalidInput)
	}
	if err := credentials.Set(credentialstore.LLMProviderTokenKey(providerID), token); err != nil {
		return fmt.Errorf("save LLM provider credential: %w", err)
	}
	return nil
}

func Delete(credentials credentialstore.Store, providerID string) error {
	if credentials == nil {
		return nil
	}
	if err := credentials.Delete(credentialstore.LLMProviderTokenKey(providerID)); err != nil {
		return fmt.Errorf("delete LLM provider credential: %w", err)
	}
	return nil
}
