package agentmodel

import (
	"encoding/json"
	"strings"
)

const functionArgumentsDecodeErrorKey = "__materialmind_function_arguments_decode_error"

func decodeFunctionArguments(raw string) map[string]any {
	arguments := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return arguments
	}
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return map[string]any{functionArgumentsDecodeErrorKey: err.Error()}
	}
	return arguments
}

// FunctionArgumentsDecodeError reports whether an adapter preserved a malformed
// function call so the engine can return a recoverable tool result.
func FunctionArgumentsDecodeError(arguments map[string]any) (string, bool) {
	message, ok := arguments[functionArgumentsDecodeErrorKey].(string)
	return message, ok && strings.TrimSpace(message) != ""
}
