package mcpruntime

import (
	"reflect"
	"testing"
)

func TestBridgeInputSchemaNormalizesTypes(t *testing.T) {
	schema, err := bridgeInputSchema(map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"query": map[string]any{"type": "STRING"},
			"limit": map[string]any{"type": []any{"INTEGER", "NULL"}},
		},
	})
	if err != nil {
		t.Fatalf("bridgeInputSchema() error = %v", err)
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
			"limit": map[string]any{"type": []any{"integer", "null"}},
		},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Fatalf("bridgeInputSchema() = %#v, want %#v", schema, want)
	}
}
