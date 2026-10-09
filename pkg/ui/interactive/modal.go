package interactive

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/ui/style"
)

type scrollBlockReplacer interface {
	ReplaceScrollBlockBack(count int, lines []string) bool
}

type approvalInputController interface {
	BeginApprovalInput() io.Reader
	EndApprovalInput()
}

type ChoiceMenuOption struct {
	Label string
	Keys  string
}

var (
	activeReaderMu         sync.RWMutex
	activeReaderProvider   func() io.Reader
	inApprovalPrompt       bool
	onApprovalPromptChange func(bool)
)

func SetActiveReaderProvider(provider func() io.Reader) {
	activeReaderMu.Lock()
	defer activeReaderMu.Unlock()
	activeReaderProvider = provider
}

func SetInApprovalPrompt(in bool) {
	activeReaderMu.Lock()
	inApprovalPrompt = in
	notify := onApprovalPromptChange
	activeReaderMu.Unlock()
	if notify != nil {
		notify(in)
	}
}

func OnApprovalPromptChange(fn func(bool)) {
	activeReaderMu.Lock()
	defer activeReaderMu.Unlock()
	onApprovalPromptChange = fn
}

func runChoiceMenu(input io.Reader, output io.Writer, theme style.UITheme, prompt string, options []ChoiceMenuOption, selected int) (int, bool) {
	if len(options) == 0 {
		return -1, true
	}
	if selected < 0 || selected >= len(options) {
		selected = 0
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	activeStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	firstRender := true
	fmt.Fprint(output, "\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h")

	renderOption := func(index int) string {
		if index == selected {
			return fmt.Sprintf("  > %s", activeStyle.Render(options[index].Label))
		}
		return fmt.Sprintf("    %s", options[index].Label)
	}

	renderMenu := func() {
		if firstRender {
			fmt.Fprint(output, "\r\x1b[K", promptStyle.Render(prompt+"\r\n"))
			for i := range options {
				fmt.Fprint(output, "\r\x1b[K")
				fmt.Fprintf(output, "%s\r\n", renderOption(i))
			}
			firstRender = false
			return
		}

		if promptWriter, ok := output.(scrollBlockReplacer); ok {
			lines := make([]string, len(options))
			for i := range options {
				lines[i] = renderOption(i)
			}
			if promptWriter.ReplaceScrollBlockBack(len(options), lines) {
				return
			}
		}

		fmt.Fprintf(output, "\x1b[%dA", len(options)+1)
		fmt.Fprint(output, "\r\x1b[K", promptStyle.Render(prompt+"\r\n"))
		for i := range options {
			fmt.Fprint(output, "\r\x1b[K")
			fmt.Fprintf(output, "%s\r\n", renderOption(i))
		}
	}

	renderMenu()
	buf := make([]byte, 3)
	for {
		n, err := input.Read(buf)
		if err != nil {
			return selected, true
		}
		if n == 0 {
			continue
		}

		if n == 3 && buf[0] == 27 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				selected = (selected - 1 + len(options)) % len(options)
			case 'B':
				selected = (selected + 1) % len(options)
			}
			renderMenu()
			continue
		}

		for _, char := range buf[:n] {
			if char == '\r' || char == '\n' {
				return selected, false
			}
			if char == 3 || char == 4 || char == 27 {
				return selected, true
			}
			for index, option := range options {
				if strings.ContainsRune(option.Keys, rune(char)) {
					return index, false
				}
			}
		}
	}
}

func runModalChoice(w io.Writer, theme style.UITheme, prompt string, options []ChoiceMenuOption, selected int) (int, bool) {
	var input io.Reader = os.Stdin
	var output io.Writer = os.Stdout
	fd := int(os.Stdin.Fd())

	if w != nil {
		output = w
		if tty, ok := w.(*os.File); ok {
			input = tty
			output = tty
			fd = int(tty.Fd())
		}
	}

	activeReaderMu.Lock()
	var activeReader io.Reader
	if activeReaderProvider != nil {
		activeReader = activeReaderProvider()
	}
	hasActiveReader := activeReader != nil
	if hasActiveReader {
		input = activeReader
		output = w
	}
	inApprovalPrompt = true
	notify := onApprovalPromptChange
	activeReaderMu.Unlock()
	if notify != nil {
		notify(true)
	}

	var inputController approvalInputController
	if controller, ok := activeReader.(approvalInputController); ok {
		inputController = controller
		if approvalInput := controller.BeginApprovalInput(); approvalInput != nil {
			input = approvalInput
		}
	}

	defer func() {
		if inputController != nil {
			inputController.EndApprovalInput()
		}
		activeReaderMu.Lock()
		inApprovalPrompt = false
		notifyEnd := onApprovalPromptChange
		activeReaderMu.Unlock()
		if notifyEnd != nil {
			notifyEnd(false)
		}
	}()

	if !hasActiveReader && input == os.Stdin {
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			input = tty
			output = tty
			fd = int(tty.Fd())
		}
	}

	isTerm := term.IsTerminal(fd)
	var oldState *term.State
	if isTerm && !hasActiveReader {
		var err error
		oldState, err = term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	// Fallback only if NOT a terminal AND no active reader
	if !isTerm && !hasActiveReader {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		shortcutLabels := make([]string, 0, len(options))
		for _, option := range options {
			key := ""
			if option.Keys != "" {
				key = string([]rune(option.Keys)[0])
			}
			shortcutLabels = append(shortcutLabels, fmt.Sprintf("%s=%s", key, option.Label))
		}
		fmt.Fprint(output, promptStyle.Render(fmt.Sprintf("%s [%s]: ", prompt, strings.Join(shortcutLabels, "/"))))
		buf := make([]byte, 1)
		n, err := input.Read(buf)
		if err != nil || n == 0 {
			return selected, true
		}
		for index, option := range options {
			if strings.ContainsRune(option.Keys, rune(buf[0])) {
				return index, false
			}
		}
		return selected, true
	}

	return runChoiceMenu(input, output, theme, prompt, options, selected)
}

func AskForApproval(w io.Writer, theme style.UITheme) (bool, bool) {
	options := []ChoiceMenuOption{
		{Label: "yes", Keys: "yY"},
		{Label: "no", Keys: "nN"},
		{Label: "always", Keys: "aA"},
	}
	selected, dismissed := runModalChoice(w, theme, " approve tool execution?", options, 0)
	if dismissed {
		return false, false
	}
	if selected == 0 {
		return true, false
	} else if selected == 2 {
		return true, true
	} else {
		return false, false
	}
}

func AskUserQuestion(w io.Writer, theme style.UITheme, question string, options []string, recommended string) (string, error) {
	var menuOptions []ChoiceMenuOption
	defaultIdx := 0

	if len(options) == 0 {
		menuOptions = []ChoiceMenuOption{
			{Label: "yes, proceed (Recommended)", Keys: "yY1"},
			{Label: "no, cancel", Keys: "nN2"},
		}
	} else {
		for i, opt := range options {
			key := fmt.Sprintf("%d", i+1)
			if strings.EqualFold(opt, recommended) || (recommended == "" && i == 0) {
				defaultIdx = i
			}
			menuOptions = append(menuOptions, ChoiceMenuOption{
				Label: opt,
				Keys:  key,
			})
		}
	}

	prompt := strings.TrimSpace(question)
	if prompt == "" {
		prompt = "Please select an option:"
	}
	selected, dismissed := runModalChoice(w, theme, " "+prompt, menuOptions, defaultIdx)
	if dismissed {
		return "Operation cancelled by user", nil
	}
	if selected >= 0 && selected < len(menuOptions) {
		return menuOptions[selected].Label, nil
	}
	return "Operation cancelled by user", nil
}

func AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) agent.SubagentCancellationDecision {
	return AskForSubagentCancellationWithReader(w, nil, theme, agentName)
}

func AskForSubagentCancellationWithReader(w io.Writer, input io.Reader, theme style.UITheme, agentName string) agent.SubagentCancellationDecision {
	options := []ChoiceMenuOption{
		{Label: fmt.Sprintf("skip current agent (%s)", agentName), Keys: "kK"},
		{Label: "stop completely", Keys: "xX"},
	}
	var selected int
	var dismissed bool
	if input == nil {
		selected, dismissed = runModalChoice(w, theme, " cancel active subagent?", options, 1)
	} else {
		SetInApprovalPrompt(true)
		defer SetInApprovalPrompt(false)
		selected, dismissed = runChoiceMenu(input, w, theme, " cancel active subagent?", options, 1)
	}
	if dismissed {
		return agent.SubagentCancellationContinue
	}
	if selected == 0 {
		return agent.SubagentCancellationSkipCurrent
	}
	return agent.SubagentCancellationStopAll
}
