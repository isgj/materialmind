package mcptools

import (
	"encoding/json"
	"testing"
)

func TestInputSchemaNormalizesMCPTypes(t *testing.T) {
	schema, err := inputSchema(json.RawMessage(
		`{"type":"OBJECT","properties":{"query":{"type":"STRING"}}}`,
	))
	if err != nil {
		t.Fatalf("inputSchema() error = %v", err)
	}
	if schema.Type != "object" ||
		schema.Properties["query"] == nil ||
		schema.Properties["query"].Type != "string" {
		t.Fatalf("inputSchema() = %#v", schema)
	}
}

func TestInputSchemaRejectsNonObjectRoot(t *testing.T) {
	if _, err := inputSchema(map[string]any{"type": "string"}); err == nil {
		t.Fatal("inputSchema() error = nil")
	}
}
