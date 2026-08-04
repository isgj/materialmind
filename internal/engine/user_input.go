package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"materialmind/internal/store"
	"materialmind/internal/workspacetools"
)

const (
	maxPendingUserInputs    = 8
	maxUserInputAnswerRunes = 2000
)

var ErrUserInputNotPending = errors.New("user input is not pending")

type UserInputRequest struct {
	ID         string                           `json:"id"`
	ToolCallID string                           `json:"toolCallId"`
	ToolName   string                           `json:"toolName"`
	Questions  []workspacetools.AskUserQuestion `json:"questions"`
}

type UserInputAnswerSubmission struct {
	QuestionID string `json:"questionId"`
	OptionID   string `json:"optionId,omitempty"`
	Text       string `json:"text,omitempty"`
}

type UserInputResolution struct {
	ID         string                         `json:"id"`
	ToolCallID string                         `json:"toolCallId"`
	Answers    []workspacetools.AskUserAnswer `json:"answers"`
}

type pendingUserInput struct {
	request  UserInputRequest
	response chan UserInputResolution
}

func (e *Engine) requestUserInput(
	ctx context.Context,
	sessionID, runID, toolCallID string,
	questions []workspacetools.AskUserQuestion,
) ([]workspacetools.AskUserAnswer, error) {
	request := UserInputRequest{
		ID:         uuid.NewString(),
		ToolCallID: toolCallID,
		ToolName:   "ask_user",
		Questions:  questions,
	}
	pending := &pendingUserInput{
		request:  request,
		response: make(chan UserInputResolution, 1),
	}

	e.mu.Lock()
	active, ok := e.active[sessionID]
	if !ok || active.runID != runID {
		e.mu.Unlock()
		return nil, ErrUserInputNotPending
	}
	if active.pendingUserInputs == nil {
		active.pendingUserInputs = make(map[string]*pendingUserInput)
	}
	if len(active.pendingUserInputs) >= maxPendingUserInputs {
		e.mu.Unlock()
		return nil, fmt.Errorf("too many pending user input requests")
	}
	active.pendingUserInputs[request.ID] = pending
	e.mu.Unlock()

	e.hub.Publish(runID, "user_input_request", request)
	defer e.removePendingUserInput(sessionID, runID, request.ID, pending)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resolution := <-pending.response:
		return resolution.Answers, nil
	}
}

func (e *Engine) ResolveUserInput(
	ctx context.Context,
	runID, requestID string,
	submissions []UserInputAnswerSubmission,
) (UserInputResolution, error) {
	runRecord, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return UserInputResolution{}, err
	}

	e.mu.Lock()
	active, ok := e.active[runRecord.SessionID]
	if !ok || active.runID != runID {
		e.mu.Unlock()
		return UserInputResolution{}, ErrUserInputNotPending
	}
	pending, ok := active.pendingUserInputs[requestID]
	if !ok {
		e.mu.Unlock()
		return UserInputResolution{}, ErrUserInputNotPending
	}
	answers, err := resolveUserInputAnswers(pending.request.Questions, submissions)
	if err != nil {
		e.mu.Unlock()
		return UserInputResolution{}, err
	}
	resolution := UserInputResolution{
		ID:         requestID,
		ToolCallID: pending.request.ToolCallID,
		Answers:    answers,
	}
	select {
	case pending.response <- resolution:
		delete(active.pendingUserInputs, requestID)
	default:
		e.mu.Unlock()
		return UserInputResolution{}, fmt.Errorf("queue user input: %w", ErrSessionBusy)
	}
	e.mu.Unlock()

	e.hub.Publish(runID, "user_input_resolved", resolution)
	return resolution, nil
}

func (e *Engine) removePendingUserInput(
	sessionID, runID, requestID string,
	pending *pendingUserInput,
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	active, ok := e.active[sessionID]
	if !ok || active.runID != runID || active.pendingUserInputs[requestID] != pending {
		return
	}
	delete(active.pendingUserInputs, requestID)
}

func resolveUserInputAnswers(
	questions []workspacetools.AskUserQuestion,
	submissions []UserInputAnswerSubmission,
) ([]workspacetools.AskUserAnswer, error) {
	if len(submissions) != len(questions) {
		return nil, fmt.Errorf(
			"%w: answers must include every question exactly once",
			store.ErrInvalidInput,
		)
	}
	byQuestion := make(map[string]UserInputAnswerSubmission, len(submissions))
	for _, submission := range submissions {
		submission.QuestionID = strings.TrimSpace(submission.QuestionID)
		submission.OptionID = strings.TrimSpace(submission.OptionID)
		submission.Text = strings.TrimSpace(submission.Text)
		if submission.QuestionID == "" {
			return nil, fmt.Errorf("%w: answer questionId is required", store.ErrInvalidInput)
		}
		if _, exists := byQuestion[submission.QuestionID]; exists {
			return nil, fmt.Errorf(
				"%w: question %q is answered more than once",
				store.ErrInvalidInput,
				submission.QuestionID,
			)
		}
		byQuestion[submission.QuestionID] = submission
	}

	answers := make([]workspacetools.AskUserAnswer, 0, len(questions))
	for _, question := range questions {
		submission, ok := byQuestion[question.ID]
		if !ok {
			return nil, fmt.Errorf(
				"%w: question %q is not answered",
				store.ErrInvalidInput,
				question.ID,
			)
		}
		if submission.OptionID != "" && submission.Text != "" {
			return nil, fmt.Errorf(
				"%w: question %q must use either an option or a custom response",
				store.ErrInvalidInput,
				question.ID,
			)
		}
		if submission.OptionID != "" {
			var selected *workspacetools.AskUserOption
			for optionIndex := range question.Options {
				if question.Options[optionIndex].ID == submission.OptionID {
					selected = &question.Options[optionIndex]
					break
				}
			}
			if selected == nil {
				return nil, fmt.Errorf(
					"%w: question %q option %q is not available",
					store.ErrInvalidInput,
					question.ID,
					submission.OptionID,
				)
			}
			answers = append(answers, workspacetools.AskUserAnswer{
				QuestionID: question.ID,
				Answer:     selected.Label,
				OptionID:   selected.ID,
			})
			continue
		}
		if submission.Text == "" {
			return nil, fmt.Errorf(
				"%w: question %q requires an answer",
				store.ErrInvalidInput,
				question.ID,
			)
		}
		if utf8.RuneCountInString(submission.Text) > maxUserInputAnswerRunes {
			return nil, fmt.Errorf(
				"%w: question %q response must be at most %d characters",
				store.ErrInvalidInput,
				question.ID,
				maxUserInputAnswerRunes,
			)
		}
		answers = append(answers, workspacetools.AskUserAnswer{
			QuestionID: question.ID,
			Answer:     submission.Text,
		})
	}
	return answers, nil
}
