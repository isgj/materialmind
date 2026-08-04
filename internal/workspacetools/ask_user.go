package workspacetools

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	maxAskUserQuestions        = 5
	maxAskUserOptions          = 6
	maxAskUserIDRunes          = 120
	maxAskUserQuestionRunes    = 500
	maxAskUserOptionLabelRunes = 100
	maxAskUserDescriptionRunes = 300
)

type AskUserOption struct {
	ID          string `json:"id" jsonschema:"Unique option identifier within this question."`
	Label       string `json:"label" jsonschema:"Short answer label shown to the user."`
	Description string `json:"description,omitempty" jsonschema:"Optional short explanation of this option."`
}

type AskUserQuestion struct {
	ID       string          `json:"id" jsonschema:"Unique question identifier within this tool call."`
	Question string          `json:"question" jsonschema:"The clarification question to show the user."`
	Options  []AskUserOption `json:"options,omitempty" jsonschema:"Suggested single-choice answers. The user can always provide a different free-text response."`
}

type AskUserArgs struct {
	Questions []AskUserQuestion `json:"questions" jsonschema:"One to five related clarification questions. Ask all currently known questions in one call."`
}

type AskUserAnswer struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
	OptionID   string `json:"optionId,omitempty"`
}

type AskUserResult struct {
	State   string          `json:"state"`
	Answers []AskUserAnswer `json:"answers"`
}

type AskUserHandler func(
	context.Context,
	string,
	[]AskUserQuestion,
) ([]AskUserAnswer, error)

func newAskUserTool(handler AskUserHandler) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name: "ask_user",
			Description: "Ask the user for clarification when a material decision cannot be inferred safely. " +
				"Ask all currently known questions in one call. Each question may offer concise options, " +
				"but the user can always provide a different response. Do not request passwords, tokens, or other secrets.",
		},
		func(ctx agent.Context, args AskUserArgs) (AskUserResult, error) {
			questions, err := normalizeAskUserQuestions(args.Questions)
			if err != nil {
				return AskUserResult{}, err
			}
			if handler == nil {
				return AskUserResult{}, fmt.Errorf("ask_user is unavailable")
			}
			answers, err := handler(ctx, ctx.FunctionCallID(), questions)
			if err != nil {
				return AskUserResult{}, err
			}
			return AskUserResult{State: "answered", Answers: answers}, nil
		},
	)
}

func normalizeAskUserQuestions(questions []AskUserQuestion) ([]AskUserQuestion, error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("ask_user requires at least one question")
	}
	if len(questions) > maxAskUserQuestions {
		return nil, fmt.Errorf("ask_user supports at most %d questions", maxAskUserQuestions)
	}

	normalized := make([]AskUserQuestion, 0, len(questions))
	seenQuestions := make(map[string]struct{}, len(questions))
	for questionIndex, question := range questions {
		question.ID = strings.TrimSpace(question.ID)
		question.Question = strings.TrimSpace(question.Question)
		if question.ID == "" {
			return nil, fmt.Errorf("question %d id is required", questionIndex+1)
		}
		if utf8.RuneCountInString(question.ID) > maxAskUserIDRunes {
			return nil, fmt.Errorf("question %q id must be at most %d characters", question.ID, maxAskUserIDRunes)
		}
		if _, exists := seenQuestions[question.ID]; exists {
			return nil, fmt.Errorf("question id %q is duplicated", question.ID)
		}
		seenQuestions[question.ID] = struct{}{}
		if question.Question == "" {
			return nil, fmt.Errorf("question %q text is required", question.ID)
		}
		if utf8.RuneCountInString(question.Question) > maxAskUserQuestionRunes {
			return nil, fmt.Errorf(
				"question %q text must be at most %d characters",
				question.ID,
				maxAskUserQuestionRunes,
			)
		}
		if len(question.Options) > maxAskUserOptions {
			return nil, fmt.Errorf(
				"question %q supports at most %d options",
				question.ID,
				maxAskUserOptions,
			)
		}

		options := make([]AskUserOption, 0, len(question.Options))
		seenOptions := make(map[string]struct{}, len(question.Options))
		for optionIndex, option := range question.Options {
			option.ID = strings.TrimSpace(option.ID)
			option.Label = strings.TrimSpace(option.Label)
			option.Description = strings.TrimSpace(option.Description)
			if option.ID == "" {
				return nil, fmt.Errorf(
					"question %q option %d id is required",
					question.ID,
					optionIndex+1,
				)
			}
			if utf8.RuneCountInString(option.ID) > maxAskUserIDRunes {
				return nil, fmt.Errorf(
					"question %q option %q id must be at most %d characters",
					question.ID,
					option.ID,
					maxAskUserIDRunes,
				)
			}
			if _, exists := seenOptions[option.ID]; exists {
				return nil, fmt.Errorf(
					"question %q option id %q is duplicated",
					question.ID,
					option.ID,
				)
			}
			seenOptions[option.ID] = struct{}{}
			if option.Label == "" {
				return nil, fmt.Errorf(
					"question %q option %q label is required",
					question.ID,
					option.ID,
				)
			}
			if utf8.RuneCountInString(option.Label) > maxAskUserOptionLabelRunes {
				return nil, fmt.Errorf(
					"question %q option %q label must be at most %d characters",
					question.ID,
					option.ID,
					maxAskUserOptionLabelRunes,
				)
			}
			if utf8.RuneCountInString(option.Description) > maxAskUserDescriptionRunes {
				return nil, fmt.Errorf(
					"question %q option %q description must be at most %d characters",
					question.ID,
					option.ID,
					maxAskUserDescriptionRunes,
				)
			}
			options = append(options, option)
		}
		question.Options = options
		normalized = append(normalized, question)
	}
	return normalized, nil
}
