package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type AskUserOption = domaintool.AskUserOption
type UserInquirer = domaintool.UserInquirer

type AskUserTool struct {
	CustomHandler func(question string, options []AskUserOption, recommended string) (string, error)
}

func NewAskUserTool() *AskUserTool {
	return &AskUserTool{}
}

func (t *AskUserTool) Name() string { return "ask_user" }

func (t *AskUserTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Ask the user a structured question for clarification, confirmation, or selecting design alternatives")
}

func (t *AskUserTool) PromptGuidelines() []string {
	return FormatToolGuidelines(t.Name(), []string{
		"Call 'ask_user' when you need user input, requirements clarification, or confirmation before destructive actions.",
		"When providing options, set 'recommended' to the exact label of your recommended option; the UI tags it as (Recommended) and pre-selects it.",
		"Do not include '(Recommended)' in option labels or add an 'Other' free-text option: the UI adds both automatically. If the user submits an empty free-text answer, ask again with an explicit prompt.",
	})
}

func (t *AskUserTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "ask_user",
			Description: FormatToolDescription("ask_user", "Ask the user a structured question for clarification, disambiguation, or confirmation. Returns the user's selected choice or answer."),
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"question": {
						Type:        "string",
						Description: FormatParamDescription("ask_user", "question", "The question or clarification request to present to the user."),
					},
					"options": {
						Type:        "array",
						Description: FormatParamDescription("ask_user", "options", "List of options for the user to select from. Can be strings or {label, description} objects. Do not manually add '(Recommended)' or 'Other'; the UI adds both."),
						Items: &SchemaProp{
							Type: "object",
							Properties: map[string]SchemaProp{
								"label": {
									Type:        "string",
									Description: FormatParamDescription("ask_user", "options.label", "Option text."),
								},
								"description": {
									Type:        "string",
									Description: FormatParamDescription("ask_user", "options.description", "Optional explanation of the option."),
								},
							},
						},
					},
					"recommended": {
						Type:        "string",
						Description: FormatParamDescription("ask_user", "recommended", "Exact label of the recommended option from 'options'. Set this whenever presenting selectable options so the UI tags and pre-selects it."),
					},
				},
				Required: []string{"question"},
			},
		},
	}
}

func parseAskUserOptions(raw json.RawMessage) ([]AskUserOption, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var strOptions []string
	if err := json.Unmarshal(raw, &strOptions); err == nil {
		parsed := make([]AskUserOption, 0, len(strOptions))
		for _, s := range strOptions {
			parsed = append(parsed, AskUserOption{Label: s})
		}
		return parsed, nil
	}
	var objOptions []AskUserOption
	if err := json.Unmarshal(raw, &objOptions); err == nil {
		return objOptions, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("invalid ask_user options: %w", err)
	}
	parsed := make([]AskUserOption, 0, len(items))
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			parsed = append(parsed, AskUserOption{Label: s})
			continue
		}
		var opt AskUserOption
		if err := json.Unmarshal(item, &opt); err != nil {
			return nil, fmt.Errorf("invalid ask_user options: %w", err)
		}
		parsed = append(parsed, opt)
	}
	return parsed, nil
}

func (t *AskUserTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var raw struct {
		Question    string          `json:"question"`
		Options     json.RawMessage `json:"options"`
		Recommended string          `json:"recommended"`
	}

	if err := ParseToolArguments(arguments, &raw, func(s string) { raw.Question = s }); err != nil {
		return "", err
	}

	if strings.TrimSpace(raw.Question) == "" {
		return "", fmt.Errorf("question cannot be empty")
	}

	parsedOptions, err := parseAskUserOptions(raw.Options)
	if err != nil {
		return "", err
	}
	parsedOptions, resolvedRecommended := ResolveAskUserRecommendation(parsedOptions, raw.Recommended)

	if t.CustomHandler != nil {
		return t.CustomHandler(raw.Question, parsedOptions, resolvedRecommended)
	}

	if uq, ok := ctx.(UserInquirer); ok {
		ans, err := uq.AskUser(raw.Question, parsedOptions, resolvedRecommended)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("User response to question %q: Selected %q", raw.Question, ans), nil
	}

	selected := DefaultAskUserSelection(parsedOptions, resolvedRecommended)
	return fmt.Sprintf("User response to question %q: Selected %q", raw.Question, selected), nil
}
