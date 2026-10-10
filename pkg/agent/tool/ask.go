package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

type AskUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

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
		"Provide clear selectable options and specify a 'recommended' choice when appropriate.",
		"The UI always appends a free-write entry ('Other: write what you want'); if the user selects it and writes nothing, ask again with an explicit prompt for the answer.",
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
						Description: FormatParamDescription("ask_user", "options", "List of options for the user to select from. Can be strings or {label, description} objects."),
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
						Description: FormatParamDescription("ask_user", "recommended", "Optional label of the recommended option."),
					},
				},
				Required: []string{"question"},
			},
		},
	}
}

// UserInquirer is an optional interface implemented by AgentContext
// to interactively ask the user questions in the terminal/TUI.
type UserInquirer interface {
	AskUser(question string, options []AskUserOption, recommended string) (string, error)
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

	var parsedOptions []AskUserOption
	if len(raw.Options) > 0 {
		var strOptions []string
		if err := json.Unmarshal(raw.Options, &strOptions); err == nil {
			for _, s := range strOptions {
				parsedOptions = append(parsedOptions, AskUserOption{Label: s})
			}
		} else {
			var objOptions []AskUserOption
			if err := json.Unmarshal(raw.Options, &objOptions); err == nil {
				parsedOptions = objOptions
			}
		}
	}

	if t.CustomHandler != nil {
		return t.CustomHandler(raw.Question, parsedOptions, raw.Recommended)
	}

	if uq, ok := ctx.(UserInquirer); ok {
		ans, err := uq.AskUser(raw.Question, parsedOptions, raw.Recommended)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("User response to question %q: Selected %q", raw.Question, ans), nil
	}

	selected := raw.Recommended
	if selected == "" && len(parsedOptions) > 0 {
		selected = parsedOptions[0].Label
	}
	if selected == "" {
		selected = "Confirmed"
	}

	return fmt.Sprintf("User response to question %q: Selected %q", raw.Question, selected), nil
}
