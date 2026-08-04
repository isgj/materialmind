package engine

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"

	"materialmind/internal/agentmodel"
)

func rejectMalformedFunctionArguments(
	_ agent.Context,
	candidate tool.Tool,
	arguments map[string]any,
) (map[string]any, error) {
	decodeError, malformed := agentmodel.FunctionArgumentsDecodeError(arguments)
	if !malformed {
		return nil, nil
	}
	return map[string]any{
		"error": fmt.Sprintf(
			"The %q tool was not run because its arguments were incomplete or invalid JSON: %s. Retry the tool call with one complete JSON object.",
			candidate.Name(),
			decodeError,
		),
	}, nil
}
