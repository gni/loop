package interactive

import (
	"bufio"
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

func getScrollBlockReplacer(w io.Writer) scrollBlockReplacer {
	type unwrapper interface {
		Unwrap() io.Writer
	}
	curr := w
	for curr != nil {
		if sbr, ok := curr.(scrollBlockReplacer); ok {
			return sbr
		}
		if u, ok := curr.(unwrapper); ok {
			next := u.Unwrap()
			if next == nil || next == curr {
				break
			}
			curr = next
		} else {
			break
		}
	}
	return nil
}

type approvalInputController interface {
	BeginApprovalInput() io.Reader
	EndApprovalInput()
}

type ChoiceMenuOption struct {
	Label string
	Keys  string
	// FreeText marks an option that, when selected, lets the user type a
	// free-form answer instead of just picking the label.
	FreeText bool
}

var (
	activeReaderMu         sync.RWMutex
	activeReaderProvider   func() io.Reader
	onApprovalPromptChange func(bool)
)

func SetActiveReaderProvider(provider func() io.Reader) {
	activeReaderMu.Lock()
	defer activeReaderMu.Unlock()
	activeReaderProvider = provider
}

func SetInApprovalPrompt(in bool) {
	activeReaderMu.Lock()
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
	idx, dismissed, _ := runChoiceMenuEx(input, output, theme, prompt, options, selected)
	return idx, dismissed
}

// runChoiceMenuEx is the extended choice menu. If a FreeText option is selected,
// the user is prompted to type an answer; the typed answer is returned in the
// third result and its index in the first.
func runChoiceMenuEx(input io.Reader, output io.Writer, theme style.UITheme, prompt string, options []ChoiceMenuOption, selected int) (int, bool, string) {
	if len(options) == 0 {
		return -1, true, ""
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

		if promptWriter := getScrollBlockReplacer(output); promptWriter != nil {
			lines := make([]string, len(options))
			for i := range options {
				lines[i] = renderOption(i)
			}
			if promptWriter.ReplaceScrollBlockBack(len(options), lines) {
				return
			}
		}

		fmt.Fprintf(output, "\x1b[%dA", len(options))
		for i := range options {
			fmt.Fprintf(output, "\r\x1b[2K%s\r\n", renderOption(i))
		}
	}

	renderMenu()
	buf := make([]byte, 3)
	for {
		n, err := input.Read(buf)
		if err != nil {
			return selected, true, ""
		}
		if n == 0 {
			continue
		}

		if n == 3 && buf[0] == 27 && (buf[1] == '[' || buf[1] == 'O') {
			switch buf[2] {
			case 'A':
				selected = (selected - 1 + len(options)) % len(options)
			case 'B':
				selected = (selected + 1) % len(options)
			}
			renderMenu()
			continue
		}

		for i, char := range buf[:n] {
			if char == '\r' || char == '\n' {
				if options[selected].FreeText {
					pending := string(buf[i+1:n])
					answer := readFreeTextAnswer(input, output, theme, promptStyle, activeStyle, pending)
					return selected, false, answer
				}
				return selected, false, ""
			}
			if char == 3 || char == 4 || char == 27 {
				return selected, true, ""
			}
			for index, option := range options {
				if strings.ContainsRune(option.Keys, rune(char)) {
					if option.FreeText {
						pending := string(buf[i+1:n]) // bytes already consumed past the keypress
						answer := readFreeTextAnswer(input, output, theme, promptStyle, activeStyle, pending)
						return index, false, answer
					}
					return index, false, ""
				}
			}
		}
	}
}

// readFreeTextAnswer reads one line of user input (echoed inline) and returns it.
// Enter confirms; Ctrl+C/Ctrl+D/Esc dismisses with an empty answer.
func readFreeTextAnswer(input io.Reader, output io.Writer, theme style.UITheme, promptStyle, activeStyle style.Style, initial string) string {
	fmt.Fprint(output, "\r\x1b[K", promptStyle.Render("  type your answer (Enter to confirm): "))

	answer := []byte{}
	for i := 0; i < len(initial); i++ {
		c := initial[i]
		if c == '\r' || c == '\n' {
			if len(answer) > 0 {
				fmt.Fprint(output, "\r\n")
				return strings.TrimSpace(string(answer))
			}
			// Leading newline confirms the menu selection; skip it.
			continue
		}
		answer = append(answer, c)
		fmt.Fprint(output, string(c))
	}
	line := make([]byte, 1)
	for {
		n, err := input.Read(line)
		if err != nil || n == 0 {
			if len(answer) > 0 {
				return strings.TrimSpace(string(answer))
			}
			return ""
		}
		c := line[0]
		switch c {
		case '\r', '\n':
			fmt.Fprint(output, "\r\n")
			if len(answer) == 0 {
				return ""
			}
			return strings.TrimSpace(string(answer))
		case 3, 4, 27:
			fmt.Fprint(output, "\r\n")
			return ""
		case 127, 8:
			if len(answer) > 0 {
				answer = answer[:len(answer)-1]
				fmt.Fprint(output, "\r\x1b[K", activeStyle.Render("  type your answer (Enter to confirm): ")+string(answer))
			}
		default:
			answer = append(answer, c)
			fmt.Fprint(output, string(c))
		}
	}
}

func runModalChoice(w io.Writer, theme style.UITheme, prompt string, options []ChoiceMenuOption, selected int) (int, bool) {
	idx, dismissed, _ := runModalChoiceEx(w, theme, prompt, options, selected)
	return idx, dismissed
}

func runModalChoiceEx(w io.Writer, theme style.UITheme, prompt string, options []ChoiceMenuOption, selected int) (int, bool, string) {
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
			return selected, true, ""
		}
		for index, option := range options {
			if strings.ContainsRune(option.Keys, rune(buf[0])) {
				if option.FreeText {
					reader := bufio.NewReader(input)
					line, _ := reader.ReadString('\n')
					return index, false, strings.TrimSpace(line)
				}
				return index, false, ""
			}
		}
		return selected, true, ""
	}

	return runChoiceMenuEx(input, output, theme, prompt, options, selected)
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
	// Always append a free-write entry so the user can type exactly what they want.
	menuOptions = append(menuOptions, ChoiceMenuOption{
		Label:    "Other: write what you want",
		Keys:     fmt.Sprintf("%d", len(menuOptions)+1),
		FreeText: true,
	})

	selected, dismissed, freeText := runModalChoiceEx(w, theme, " "+prompt, menuOptions, defaultIdx)
	if dismissed {
		return "operation cancelled by user", nil
	}
	if selected >= 0 && selected < len(menuOptions) {
		if menuOptions[selected].FreeText {
			if freeText == "" {
				return "other: wrote nothing", nil
			}
			return freeText, nil
		}
		return menuOptions[selected].Label, nil
	}
	return "operation cancelled by user", nil
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
