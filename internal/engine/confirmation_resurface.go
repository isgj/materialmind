package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

// resurfaceConfirmation describes a tool confirmation that an agent iteration
// reported through EventActions.RequestedToolConfirmations but that ADK did
// not surface as an adk_request_confirmation event.
type resurfaceConfirmation struct {
	callID       string
	confirmation toolconfirmation.ToolConfirmation
	originalCall *genai.FunctionCall
	source       *session.Event
}

// danglingConfirmationRequests reports the confirmation requests of a
// completed agent iteration that ADK did not surface as
// adk_request_confirmation events.
//
// ADK emits those events only from the regular model step. When a tool
// resumed after the user answered an earlier confirmation requests a new
// confirmation of its own (for example fetch_url following a redirect to an
// unapproved URL), the resume path yields only the tool response event, whose
// Actions.RequestedToolConfirmations carries the request. A request counts as
// surfaced when an event of the same iteration carries an
// adk_request_confirmation call for the original call.
func danglingConfirmationRequests(events []*session.Event) []resurfaceConfirmation {
	surfaced := make(map[string]struct{})
	for _, event := range events {
		if event == nil || event.Partial || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionCall == nil || !isConfirmationCall(part.FunctionCall.Name) {
				continue
			}
			original, err := toolconfirmation.OriginalCallFrom(part.FunctionCall)
			if err != nil || original == nil || original.ID == "" {
				continue
			}
			surfaced[original.ID] = struct{}{}
		}
	}
	requests := make([]resurfaceConfirmation, 0)
	seen := make(map[string]struct{})
	for _, event := range events {
		if event == nil || event.Partial {
			continue
		}
		for _, callID := range sortedConfirmationCallIDs(event.Actions.RequestedToolConfirmations) {
			if _, ok := seen[callID]; ok {
				continue
			}
			if _, ok := surfaced[callID]; ok {
				continue
			}
			seen[callID] = struct{}{}
			requests = append(requests, resurfaceConfirmation{
				callID:       callID,
				confirmation: event.Actions.RequestedToolConfirmations[callID],
				source:       event,
			})
		}
	}
	return requests
}

// buildResurfaceEvent assembles the adk_request_confirmation event ADK's
// resume path omits, mirroring the event ADK emits from the model step: one
// function call per confirmation request, each carrying the original call and
// the requested confirmation, with the original calls marked as long running.
func buildResurfaceEvent(ctx context.Context, agentName string, requests []resurfaceConfirmation) *session.Event {
	source := requests[0].source
	parts := make([]*genai.Part, 0, len(requests))
	toolIDs := make([]string, 0, len(requests))
	for _, request := range requests {
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   uuid.NewString(),
			Name: toolconfirmation.FunctionCallName,
			Args: map[string]any{
				"originalFunctionCall": request.originalCall,
				"toolConfirmation": toolconfirmation.ToolConfirmation{
					Hint:    request.confirmation.Hint,
					Payload: request.confirmation.Payload,
				},
			},
		}})
		toolIDs = append(toolIDs, request.callID)
	}
	event := session.NewEvent(ctx, source.InvocationID)
	event.Author = source.Author
	if event.Author == "" {
		event.Author = agentName
	}
	event.Branch = source.Branch
	event.IsolationScope = source.IsolationScope
	event.LongRunningToolIDs = toolIDs
	event.LLMResponse = model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: parts},
	}
	return event
}

// resurfaceEventGroups splits requests into the synthetic confirmation events
// they belong to. Requests that share the invocation, branch, and isolation
// scope of their source events are bundled into one event, so a single event
// never mixes metadata from two agent scopes.
func resurfaceEventGroups(requests []resurfaceConfirmation) [][]resurfaceConfirmation {
	type scope struct {
		invocationID   string
		branch         string
		isolationScope string
	}
	groups := make([][]resurfaceConfirmation, 0)
	positions := make(map[scope]int)
	for _, request := range requests {
		key := scope{
			invocationID:   request.source.InvocationID,
			branch:         request.source.Branch,
			isolationScope: request.source.IsolationScope,
		}
		position, ok := positions[key]
		if !ok {
			position = len(groups)
			positions[key] = position
			groups = append(groups, nil)
		}
		groups[position] = append(groups[position], request)
	}
	return groups
}

// findFunctionCall returns the first function call part with the given ID in
// the event history, or nil when the call is not present.
func findFunctionCall(events []*session.Event, callID string) *genai.FunctionCall {
	for _, event := range events {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil && part.FunctionCall.ID == callID {
				return part.FunctionCall
			}
		}
	}
	return nil
}

// attachOriginalCalls attaches each request's original function call from the
// session history and drops requests whose call is not present, so a resurface
// gap stays observable instead of silently losing the approval.
func attachOriginalCalls(sessionID string, sessionEvents session.Events, requests []resurfaceConfirmation) []resurfaceConfirmation {
	events := make([]*session.Event, 0, sessionEvents.Len())
	for event := range sessionEvents.All() {
		events = append(events, event)
	}
	kept := make([]resurfaceConfirmation, 0, len(requests))
	for _, request := range requests {
		original := findFunctionCall(events, request.callID)
		if original == nil {
			slog.Warn(
				"skipping resumed tool confirmation without an original call in the session",
				"session_id", sessionID,
				"tool_call_id", request.callID,
			)
			continue
		}
		request.originalCall = original
		kept = append(kept, request)
	}
	return kept
}

// sortedConfirmationCallIDs returns the call IDs of the requested
// confirmations in deterministic order.
func sortedConfirmationCallIDs(confirmations map[string]toolconfirmation.ToolConfirmation) []string {
	ids := make([]string, 0, len(confirmations))
	for callID := range confirmations {
		ids = append(ids, callID)
	}
	slices.Sort(ids)
	return ids
}

// resurfaceResumedConfirmations appends the adk_request_confirmation events
// ADK's resume path omits and registers the approvals they represent. When a
// tool resumed after the user answered an earlier confirmation requests a new
// confirmation of its own (for example fetch_url following a redirect to an
// unapproved URL), the run would otherwise end with the call still blocked on
// an approval the user was never asked for. Appending the confirmation event
// mirrors the event ADK emits from the model step, so the next runner.Run
// resumes the original call exactly as it does after a first confirmation.
func (e *Engine) resurfaceResumedConfirmations(
	ctx context.Context,
	runRecord store.Run,
	agentName string,
	iterationEvents []*session.Event,
	pendingApprovals map[string]ToolApprovalRequest,
) error {
	requests := danglingConfirmationRequests(iterationEvents)
	if len(requests) == 0 {
		return nil
	}
	response, err := e.sessionService.Get(ctx, &session.GetRequest{
		AppName:   AppName,
		UserID:    UserID,
		SessionID: runRecord.SessionID,
	})
	if err != nil {
		return fmt.Errorf("load session for resumed confirmations: %w", err)
	}
	return e.resurfaceDanglingConfirmations(
		ctx,
		runRecord,
		agentName,
		runRecord.SessionID,
		response.Session.Events(),
		requests,
		func(event *session.Event) error {
			if err := e.sessionService.AppendEvent(ctx, response.Session, event); err != nil {
				return fmt.Errorf("append resumed confirmation event: %w", err)
			}
			return nil
		},
		func(event *session.Event) error {
			e.publishEvent(runRecord, event)
			return nil
		},
		pendingApprovals,
	)
}

// resurfaceDanglingConfirmations builds one synthetic
// adk_request_confirmation event per agent scope of the dangling requests,
// appends each event to the agent's session, registers the approvals they
// represent, and hands each event to deliverEvent so the caller can surface
// it in its own scope (parent run event, delegated transcript). Requests
// whose original call is missing from the session history are dropped with a
// warning so the resurface gap stays observable.
func (e *Engine) resurfaceDanglingConfirmations(
	ctx context.Context,
	runRecord store.Run,
	agentName string,
	sessionID string,
	sessionEvents session.Events,
	requests []resurfaceConfirmation,
	appendEvent func(event *session.Event) error,
	deliverEvent func(event *session.Event) error,
	pendingApprovals map[string]ToolApprovalRequest,
) error {
	kept := attachOriginalCalls(sessionID, sessionEvents, requests)
	if len(kept) == 0 {
		return nil
	}
	for _, group := range resurfaceEventGroups(kept) {
		event := buildResurfaceEvent(ctx, agentName, group)
		if err := appendEvent(event); err != nil {
			return err
		}
		if err := e.registerResurfacedApprovals(runRecord, event, pendingApprovals); err != nil {
			return err
		}
		if err := deliverEvent(event); err != nil {
			return err
		}
	}
	return nil
}

// registerResurfacedApprovals registers the approvals represented by a
// resurfaced confirmation event so the normal resolution flow can unblock the
// resumed tool call.
func (e *Engine) registerResurfacedApprovals(
	runRecord store.Run,
	event *session.Event,
	pendingApprovals map[string]ToolApprovalRequest,
) error {
	requests, err := toolApprovalRequests(event)
	if err != nil {
		return fmt.Errorf("decode resumed confirmation requests: %w", err)
	}
	for _, request := range requests {
		if err := e.registerToolApproval(runRecord.SessionID, runRecord.ID, request); err != nil {
			return err
		}
		pendingApprovals[request.ID] = request
	}
	return nil
}
