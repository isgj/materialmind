package workspacetools

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeAskUserQuestions(t *testing.T) {
	questions, err := normalizeAskUserQuestions([]AskUserQuestion{{
		ID:       " deployment ",
		Question: " Where should this run? ",
		Options: []AskUserOption{{
			ID:          " local ",
			Label:       " Local ",
			Description: " Run on this machine. ",
		}},
	}})
	if err != nil {
		t.Fatalf("normalizeAskUserQuestions() error = %v", err)
	}
	if len(questions) != 1 ||
		questions[0].ID != "deployment" ||
		questions[0].Question != "Where should this run?" ||
		len(questions[0].Options) != 1 ||
		questions[0].Options[0].ID != "local" ||
		questions[0].Options[0].Label != "Local" ||
		questions[0].Options[0].Description != "Run on this machine." {
		t.Fatalf("normalizeAskUserQuestions() = %#v", questions)
	}
}

func TestNormalizeAskUserQuestionsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		questions []AskUserQuestion
		wantError string
	}{
		{name: "empty", wantError: "at least one question"},
		{
			name: "duplicate question",
			questions: []AskUserQuestion{
				{ID: "target", Question: "First?"},
				{ID: "target", Question: "Second?"},
			},
			wantError: "duplicated",
		},
		{
			name: "duplicate option",
			questions: []AskUserQuestion{{
				ID:       "target",
				Question: "Where?",
				Options: []AskUserOption{
					{ID: "local", Label: "Local"},
					{ID: "local", Label: "Also local"},
				},
			}},
			wantError: "duplicated",
		},
		{
			name: "missing option label",
			questions: []AskUserQuestion{{
				ID:       "target",
				Question: "Where?",
				Options:  []AskUserOption{{ID: "local"}},
			}},
			wantError: "label is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeAskUserQuestions(test.questions)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("normalizeAskUserQuestions() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestAskUserToolDelegatesToHandler(t *testing.T) {
	var gotCallID string
	var gotQuestions []AskUserQuestion
	toolValue, err := newAskUserTool(func(
		_ context.Context,
		callID string,
		questions []AskUserQuestion,
	) ([]AskUserAnswer, error) {
		gotCallID = callID
		gotQuestions = questions
		return []AskUserAnswer{{
			QuestionID: "target",
			Answer:     "Local",
			OptionID:   "local",
		}}, nil
	})
	if err != nil {
		t.Fatalf("newAskUserTool() error = %v", err)
	}
	runnable, ok := toolValue.(runnableFunctionTool)
	if !ok {
		t.Fatalf("newAskUserTool() type = %T, want runnableFunctionTool", toolValue)
	}
	ctx := &fetchTestContext{functionCallID: "call-1"}
	result, err := runnable.Run(ctx, map[string]any{
		"questions": []any{map[string]any{
			"id":       "target",
			"question": "Where?",
			"options": []any{map[string]any{
				"id": "local", "label": "Local",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotCallID != "call-1" ||
		len(gotQuestions) != 1 ||
		result["state"] != "answered" {
		t.Fatalf("Run() call id = %q, questions = %#v, result = %#v", gotCallID, gotQuestions, result)
	}
}
