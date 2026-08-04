package llmcredentials

import (
	"errors"
	"testing"

	"materialmind/internal/credentialstore"
	"materialmind/internal/store"
)

func TestResolveKeyringCredential(t *testing.T) {
	credentials := credentialstore.NewMemory()
	provider := store.LLMProvider{
		ID:       "provider-1",
		Name:     "Gateway",
		AuthType: store.LLMAuthBearerKeyring,
	}
	if err := Set(credentials, provider.ID, "  secret-token  "); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	token, err := Resolve(credentials, provider)
	if err != nil || token != "secret-token" {
		t.Fatalf("Resolve() = %q, %v", token, err)
	}
	available, backend, err := Available(credentials, provider)
	if err != nil || !available || backend != "memory" {
		t.Fatalf("Available() = %v, %q, %v", available, backend, err)
	}
}

func TestResolveMissingKeyringCredential(t *testing.T) {
	_, err := Resolve(credentialstore.NewMemory(), store.LLMProvider{
		ID:       "provider-1",
		Name:     "Gateway",
		AuthType: store.LLMAuthBearerKeyring,
	})
	if !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("Resolve() error = %v, want invalid input", err)
	}
}

func TestResolveEnvironmentCredential(t *testing.T) {
	t.Setenv("MATERIALMIND_LLM_TOKEN_TEST", "environment-token")
	token, err := Resolve(nil, store.LLMProvider{
		AuthType:          store.LLMAuthBearerEnv,
		BearerTokenEnvVar: "MATERIALMIND_LLM_TOKEN_TEST",
	})
	if err != nil || token != "environment-token" {
		t.Fatalf("Resolve() = %q, %v", token, err)
	}
}
