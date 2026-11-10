package interactive

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/terminal"
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

type modalOutput struct {
	io.Writer
	err error
}

func (w *modalOutput) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func (w *modalOutput) Unwrap() io.Writer { return w.Writer }

var (
	activeReaderMu          sync.RWMutex
	activeReaderProvider    func() io.Reader
	onApprovalPromptChange  func(bool)
	onInteractiveMenuChange func(bool)
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

func SetInInteractiveMenu(in bool) {
	activeReaderMu.Lock()
	notify := onInteractiveMenuChange
	activeReaderMu.Unlock()
	if notify != nil {
		notify(in)
	}
}

func OnInteractiveMenuChange(fn func(bool)) {
	activeReaderMu.Lock()
	defer activeReaderMu.Unlock()
	onInteractiveMenuChange = fn
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
	if output == nil {
		output = os.Stdout
	}
	checkedOutput := &modalOutput{Writer: output}
	output = checkedOutput

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	activeStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	firstRender := true
	fmt.Fprint(output, "\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h")

	renderOption := func(index int) string {
		width, _ := style.GetTerminalSize()
		label := menuDisplayText(options[index].Label)
		if width > 5 {
			label = style.TruncateCells(label, width-5)
		}
		if index == selected {
			return fmt.Sprintf("  > %s", activeStyle.Render(label))
		}
		return fmt.Sprintf("    %s", label)
	}

	renderMenu := func() {
		if firstRender {
			prompt = menuDisplayText(prompt)
			width, _ := style.GetTerminalSize()
			if width > 2 {
				prompt = style.TruncateCells(prompt, width-2)
			}
			fmt.Fprintf(output, "\r\x1b[K%s\r\n", promptStyle.Render(prompt))
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

	sr, closeReader := modalSessionReader(input)
	defer closeReader()
	renderMenu()
	if checkedOutput.err != nil {
		return selected, true, ""
	}
	return readChoiceSelection(sr, output, promptStyle, activeStyle, options, selected, func(index int) { selected = index; renderMenu() }, true)
}

func readChoiceSelection(sr *SessionReader, output io.Writer, promptStyle, activeStyle style.Style, options []ChoiceMenuOption, selected int, redraw func(int), allowEnter bool) (int, bool, string) {
	selectOption := func(index int) (int, bool, string) {
		if options[index].FreeText {
			answer, dismissed := readFreeTextAnswer(sr, output, promptStyle, activeStyle)
			return index, dismissed, answer
		}
		return index, false, ""
	}
	for {
		if checked, ok := output.(*modalOutput); ok && checked.err != nil {
			return selected, true, ""
		}
		key, _, err := sr.ReadKeyOrResize(nil)
		if err != nil {
			return selected, true, ""
		}
		switch string(key) {
		case "\x1b[A", "\x1bOA":
			selected = (selected - 1 + len(options)) % len(options)
			if redraw != nil {
				redraw(selected)
			}
			continue
		case "\x1b[B", "\x1bOB":
			selected = (selected + 1) % len(options)
			if redraw != nil {
				redraw(selected)
			}
			continue
		case "\r", "\n":
			if allowEnter {
				return selectOption(selected)
			}
			continue
		case "\x03", "\x04", "\x1b":
			return selected, true, ""
		}
		// Paste frames and terminal reports cannot authorize a choice.
		if len(key) == 0 || key[0] == 27 {
			continue
		}
		r, size := utf8.DecodeRune(key)
		if r == utf8.RuneError && size == 1 {
			continue
		}
		for index, option := range options {
			if strings.ContainsRune(option.Keys, r) {
				return selectOption(index)
			}
		}
	}
}

// EOF, cancellation and interrupted input dismiss the editor. Only Enter
// submits the answer, including when its preceding bytes came from a paste.
func readFreeTextAnswer(sr *SessionReader, output io.Writer, promptStyle, activeStyle style.Style) (string, bool) {
	if writer := getScrollBlockReplacer(output); writer != nil {
		if cursor, ok := writer.(interface{ SetRestoreCursorToPrompt(bool) }); ok {
			cursor.SetRestoreCursorToPrompt(false)
			defer cursor.SetRestoreCursorToPrompt(true)
		}
	}
	const prefix = "  type your answer (Enter to confirm): "
	if _, err := fmt.Fprint(output, "\r\x1b[K", promptStyle.Render(prefix), "\x1b[?25h"); err != nil {
		return "", true
	}
	var answer strings.Builder
	redraw := func() bool {
		visible := strings.NewReplacer("\n", " ", "\t", " ").Replace(answer.String())
		_, err := fmt.Fprint(output, "\r\x1b[K", activeStyle.Render(prefix), visible)
		return err == nil
	}
	for {
		key, _, err := sr.ReadKeyOrResize(nil)
		if err != nil {
			return "", true
		}
		if pasted, err := appendPastedInput(key, &answer, io.Discard); pasted {
			if err != nil || !redraw() {
				return "", true
			}
			continue
		}
		switch string(key) {
		case "\r", "\n":
			if _, err := fmt.Fprint(output, "\r\n"); err != nil {
				return "", true
			}
			return strings.TrimSpace(answer.String()), false
		case "\x03", "\x04", "\x1b":
			fmt.Fprint(output, "\r\n")
			return "", true
		case "\x7f", "\x08":
			PopRuneFromBuilder(&answer, io.Discard)
			if !redraw() {
				return "", true
			}
			continue
		}
		if len(key) == 0 || key[0] == 27 {
			continue
		}
		r, size := utf8.DecodeRune(key)
		if unicode.IsControl(r) || (r == utf8.RuneError && size == 1) {
			continue
		}
		if len(key) > maxMenuInputBytes-answer.Len() {
			return "", true
		}
		answer.Write(key)
		if _, err := output.Write(key); err != nil {
			return "", true
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
		if w != nil {
			output = w
		}
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
	// The active reader routes keystrokes; it does not own terminal mode.
	// Slash commands run after ReadLine restores canonical input and echo.
	// Capture and restore the current mode for both routed and direct input.
	if isTerm {
		var err error
		oldState, err = term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	sr, closeReader := modalSessionReader(input)
	defer closeReader()
	checkedOutput := &modalOutput{Writer: output}
	output = checkedOutput
	if isTerm {
		restorePaste := terminal.Default().BeginBracketedPaste(output)
		defer restorePaste()
	}

	// A free-text-only question is already an editor, not a choice menu.
	// Open it immediately so users can type without selecting "Other" first.
	if len(options) == 1 && options[0].FreeText {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		activeStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		fmt.Fprintln(output, promptStyle.Render(menuDisplayText(prompt)))
		answer, dismissed := readFreeTextAnswer(sr, output, promptStyle, activeStyle)
		return 0, dismissed, answer
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
			label := menuDisplayText(option.Label)
			if key != "" {
				label = key + "=" + label
			}
			shortcutLabels = append(shortcutLabels, label)
		}
		fmt.Fprint(output, promptStyle.Render(fmt.Sprintf("%s [%s]: ", menuDisplayText(prompt), strings.Join(shortcutLabels, "/"))))
		return readChoiceSelection(sr, output, promptStyle, style.NewStyle().Foreground(theme.Highlight).Bold(true), options, selected, nil, false)
	}

	return runChoiceMenuEx(sr, output, theme, prompt, options, selected)
}

func AskForApproval(w io.Writer, theme style.UITheme, allowSession bool) (bool, bool) {
	options := []ChoiceMenuOption{
		{Label: "yes, proceed (Recommended)", Keys: "yY1"},
		{Label: "no, cancel", Keys: "nN2"},
	}
	if allowSession {
		options = append(options, ChoiceMenuOption{Label: "always allow in this session", Keys: "aA3"})
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

// Single-key shortcuts stop at nine; later choices remain reachable by arrows.
func choiceShortcut(index int) string {
	if index >= 0 && index < 9 {
		return fmt.Sprint(index + 1)
	}
	return ""
}

func AskUserQuestion(w io.Writer, theme style.UITheme, question string, options []string, recommended string) (string, error) {
	const freeTextLabel = "Other: write what you want"
	menuOptions := make([]ChoiceMenuOption, 0, len(options)+1)
	seen := make(map[string]bool, len(options))
	defaultIdx := 0
	recommended = strings.TrimSpace(recommended)
	recommendedLower := strings.ToLower(recommended)
	preferFreeText := recommendedLower == "other" || strings.HasPrefix(recommendedLower, "other:") || strings.HasPrefix(recommendedLower, "other (")

	for _, option := range options {
		label := strings.TrimSpace(option)
		key := strings.ToLower(label)
		if label == "" || seen[key] {
			continue
		}
		seen[key] = true

		// A model may supply its own "Other" option. The UI owns that choice
		// so it always opens the free-text editor instead of returning a label.
		if key == "other" || strings.HasPrefix(key, "other:") || strings.HasPrefix(key, "other (") {
			continue
		}
		if strings.EqualFold(label, recommended) {
			defaultIdx = len(menuOptions)
		}
		menuOptions = append(menuOptions, ChoiceMenuOption{
			Label: label,
			Keys:  choiceShortcut(len(menuOptions)),
		})
	}

	if preferFreeText {
		defaultIdx = len(menuOptions)
	}
	menuOptions = append(menuOptions, ChoiceMenuOption{
		Label:    freeTextLabel,
		Keys:     choiceShortcut(len(menuOptions)),
		FreeText: true,
	})

	prompt := strings.TrimSpace(question)
	if prompt == "" {
		prompt = "Please select an option:"
	}

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
