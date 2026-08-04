package engine

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"materialmind/internal/agentmodel"
)

// mixedToolBatchModel keeps long-running delegations out of ADK's response
// fan-in with ordinary tools. Independent delegations remain in one batch.
type mixedToolBatchModel struct {
	model.LLM

	mu      sync.Mutex
	pending *model.LLMResponse
}

func (m *mixedToolBatchModel) GenerateContent(
	ctx context.Context,
	request *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if pending := m.takePending(); pending != nil {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			yield(pending, nil)
			return
		}
		for response, err := range m.LLM.GenerateContent(ctx, request, stream) {
			if err != nil {
				yield(nil, err)
				return
			}
			immediate, deferred, splitErr := splitMixedToolBatch(response)
			if splitErr != nil {
				yield(nil, splitErr)
				return
			}
			if deferred != nil {
				if storeErr := m.storePending(deferred); storeErr != nil {
					yield(nil, storeErr)
					return
				}
				yield(immediate, nil)
				return
			}
			if !yield(response, nil) {
				return
			}
		}
	}
}

func (m *mixedToolBatchModel) takePending() *model.LLMResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := m.pending
	m.pending = nil
	return pending
}

func (m *mixedToolBatchModel) storePending(response *model.LLMResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return fmt.Errorf("a deferred delegation batch is already pending")
	}
	m.pending = response
	return nil
}

func splitMixedToolBatch(
	response *model.LLMResponse,
) (*model.LLMResponse, *model.LLMResponse, error) {
	if response == nil || response.Partial || response.Content == nil {
		return response, nil, nil
	}
	hasDelegation := false
	hasOrdinaryCall := false
	for _, part := range response.Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		if _, delegation := subAgentProfileForName(part.FunctionCall.Name); delegation {
			hasDelegation = true
		} else {
			hasOrdinaryCall = true
		}
	}
	if !hasDelegation || !hasOrdinaryCall {
		return response, nil, nil
	}

	immediateParts := make([]*genai.Part, 0, len(response.Content.Parts))
	deferredParts := make([]*genai.Part, 0, len(response.Content.Parts))
	deferredIDs := make(map[string]struct{})
	for _, part := range response.Content.Parts {
		if part == nil {
			immediateParts = append(immediateParts, nil)
			continue
		}
		cloned := *part
		if part.FunctionCall != nil {
			if _, delegation := subAgentProfileForName(part.FunctionCall.Name); delegation {
				deferredParts = append(deferredParts, &cloned)
				deferredIDs[part.FunctionCall.ID] = struct{}{}
				continue
			}
		}
		immediateParts = append(immediateParts, &cloned)
	}

	immediate := *response
	immediateContent := *response.Content
	immediateContent.Parts = immediateParts
	if err := agentmodel.FilterPreservedResponseFunctionCalls(
		&immediateContent,
		deferredIDs,
	); err != nil {
		return nil, nil, fmt.Errorf("split mixed tool response context: %w", err)
	}
	immediate.Content = &immediateContent

	deferredContent := *response.Content
	deferredContent.Parts = deferredParts
	deferred := &model.LLMResponse{
		Content:      &deferredContent,
		ModelVersion: response.ModelVersion,
		TurnComplete: true,
		FinishReason: response.FinishReason,
	}
	return &immediate, deferred, nil
}
