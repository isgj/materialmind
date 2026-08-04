package credentialstore

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/zalando/go-keyring"
)

const ServiceName = "MaterialMind"

var ErrNotFound = errors.New("credential not found")

type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	Backend() string
}

type osKeyring struct{}

func (osKeyring) Get(key string) (string, error) {
	value, err := keyring.Get(ServiceName, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read OS credential: %w", err)
	}
	return value, nil
}

func (osKeyring) Set(key, value string) error {
	if err := keyring.Set(ServiceName, key, value); err != nil {
		return fmt.Errorf("save OS credential: %w", err)
	}
	return nil
}

func (osKeyring) Delete(key string) error {
	if err := keyring.Delete(ServiceName, key); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete OS credential: %w", err)
	}
	return nil
}

func (osKeyring) Backend() string { return "os_keyring" }

type MemoryStore struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewMemory() *MemoryStore {
	return &MemoryStore{values: make(map[string]string)}
}

func (s *MemoryStore) Get(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func (s *MemoryStore) Backend() string { return "memory" }

type AutoStore struct {
	mu       sync.RWMutex
	primary  Store
	fallback *MemoryStore
	degraded bool
}

func New(mode string) (Store, error) {
	switch mode {
	case "", "auto":
		return &AutoStore{
			primary:  osKeyring{},
			fallback: NewMemory(),
		}, nil
	case "keyring":
		return osKeyring{}, nil
	case "memory":
		return NewMemory(), nil
	default:
		return nil, fmt.Errorf("unsupported credential store %q", mode)
	}
}

func (s *AutoStore) Get(key string) (string, error) {
	if s.isDegraded() {
		return s.fallback.Get(key)
	}
	value, err := s.primary.Get(key)
	if err == nil || errors.Is(err, ErrNotFound) {
		return value, err
	}
	s.degrade(err)
	return s.fallback.Get(key)
}

func (s *AutoStore) Set(key, value string) error {
	if s.isDegraded() {
		return s.fallback.Set(key, value)
	}
	if err := s.primary.Set(key, value); err != nil {
		s.degrade(err)
		return s.fallback.Set(key, value)
	}
	return nil
}

func (s *AutoStore) Delete(key string) error {
	if s.isDegraded() {
		return s.fallback.Delete(key)
	}
	if err := s.primary.Delete(key); err != nil {
		s.degrade(err)
		return s.fallback.Delete(key)
	}
	return s.fallback.Delete(key)
}

func (s *AutoStore) Backend() string {
	if s.isDegraded() {
		return s.fallback.Backend()
	}
	return s.primary.Backend()
}

func (s *AutoStore) isDegraded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degraded
}

func (s *AutoStore) degrade(err error) {
	s.mu.Lock()
	alreadyDegraded := s.degraded
	s.degraded = true
	s.mu.Unlock()
	if !alreadyDegraded {
		slog.Warn(
			"OS credential store is unavailable; credentials will be kept in memory only",
			"error",
			err,
		)
	}
}

func RefreshTokenKey(serverID string) string {
	return "mcp-oauth:" + serverID + ":refresh-token"
}

func ClientSecretKey(serverID string) string {
	return "mcp-oauth:" + serverID + ":client-secret"
}

func LLMProviderTokenKey(providerID string) string {
	return "llm-provider:" + providerID + ":bearer-token"
}
