package credentialstore

import (
	"errors"
	"testing"
)

type failingStore struct{}

func (failingStore) Get(string) (string, error) { return "", errors.New("unavailable") }
func (failingStore) Set(string, string) error   { return errors.New("unavailable") }
func (failingStore) Delete(string) error        { return errors.New("unavailable") }
func (failingStore) Backend() string            { return "failing" }

func TestAutoStoreFallsBackToMemory(t *testing.T) {
	store := &AutoStore{primary: failingStore{}, fallback: NewMemory()}
	if err := store.Set("token", "secret"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if store.Backend() != "memory" {
		t.Fatalf("Backend() = %q, want memory", store.Backend())
	}
	value, err := store.Get("token")
	if err != nil || value != "secret" {
		t.Fatalf("Get() = %q, %v", value, err)
	}
	if err := store.Delete("token"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get("token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after Delete error = %v, want ErrNotFound", err)
	}
}
