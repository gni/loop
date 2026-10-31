package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
)

// toolStreamSequencer keeps each tool call's argument fragments together.
// Later calls wait until the current call has complete JSON arguments, so
// interleaved provider deltas cannot mix different files in one preview.
type toolStreamSequencer struct {
	renderer        StreamRenderer
	enabled         bool
	active          int
	deferFollowing  bool
	livePreview     bool
	calls           map[int]*toolStreamState
	sourceIndexes   map[int]int
	unnamed         map[int][]StreamChunk
	unnamedDraft    map[int]*toolStreamState
	batch           *ToolBatch
	onProgress      func(index int, name, preview string, size int)
	onLiveContent   func(index int, name, path, delta string)
	editDiffPreview func(toolName, arguments string) string
	onReady         func(index int, name, arguments string)
}

type toolStreamState struct {
	name          string
	events        []StreamChunk
	arguments     strings.Builder
	jsonStack     []byte
	jsonStarted   bool
	inString      bool
	escaped       bool
	complete      bool
	rawTarget     bool
	payloadKnown  bool
	editDiffShown bool
	readyNotified bool
	draft         streamedDraft
}

// streamedDraft decodes readable argument values and keeps bounded excerpts.
// It does not require the filename to be known and never stores whole files.
type streamedDraft struct {
	toolName         string
	unicodeDigits    string
	unicodeRemaining int
	highSurrogate    rune
	inString         bool
	escaped          bool
	readingKey       bool
	readingBody      bool
	valueNext        bool
	key              strings.Builder
	lastKey          string
	readingPath      bool
	pathBuf          strings.Builder
	path             string
	edit             bool
	linePrefix       string
	lineStart        bool
	tail             []rune
	liveTail         []rune
	liveHead         int
	pending          strings.Builder
}

func (d *streamedDraft) appendVisible(r rune) {
	// Edit previews are proposals, not applied changes. Prefix both sides of
	// each replacement so a streaming edit is readable before execution.
	if d.edit && d.readingBody && d.lineStart {
		for _, prefixRune := range d.linePrefix {
			d.appendPlain(prefixRune)
		}
		d.lineStart = false
	}
	if d.edit && r == '\n' {
		d.lineStart = true
	}
	d.appendPlain(r)
}

func (d *streamedDraft) appendPlain(r rune) {
	if r == '\r' || r == '\t' {
		r = ' '
	}
	if (r < 32 && r != '\n') || r == 127 || r == '\x1b' {
		return
	}
	if len(d.liveTail) < 4096 {
		d.liveTail = append(d.liveTail, r)
	} else {
		d.liveTail[d.liveHead] = r
		d.liveHead = (d.liveHead + 1) % len(d.liveTail)
	}
	d.pending.WriteRune(r)
	if r == '\n' {
		r = ' '
	}
	d.tail = append(d.tail, r)
	if len(d.tail) > 96 {
		d.tail = append([]rune(nil), d.tail[len(d.tail)-96:]...)
	}
}

func (d *streamedDraft) appendJSON(fragment string) {
	for _, r := range fragment {
		if d.inString {
			if d.unicodeRemaining > 0 {
				d.unicodeDigits += string(r)
				d.unicodeRemaining--
				if d.unicodeRemaining == 0 {
					value, err := strconv.ParseUint(d.unicodeDigits, 16, 16)
					if err == nil {
						d.appendUnicode(rune(value))
					}
				}
				continue
			}
			if d.escaped {
				d.escaped = false
				if r != 'u' && d.highSurrogate != 0 {
					d.appendDecoded(utf8.RuneError)
					d.highSurrogate = 0
				}
				switch r {
				case 'u':
					d.unicodeRemaining = 4
					d.unicodeDigits = ""
				case 'n':
					d.appendDecoded('\n')
				case 't':
					d.appendDecoded('\t')
				case 'r':
					d.appendDecoded('\r')
				case '"', '\\', '/':
					d.appendDecoded(r)
				}

				continue
			}
			if r == '\\' {
				d.escaped = true
				continue
			}
			if r == '"' {
				if d.highSurrogate != 0 {
					d.appendDecoded(utf8.RuneError)
					d.highSurrogate = 0
				}
				if d.toolName == "todo" && d.readingBody && d.lastKey != "markdown" {
					d.appendPlain('\n')
				}
				if d.edit && d.readingBody && !d.lineStart {
					d.appendPlain('\n')
					d.lineStart = true
				}
				d.inString = false
				if d.readingKey {
					d.lastKey = d.key.String()
					d.key.Reset()
				}
				if d.readingPath {
					d.path = d.pathBuf.String()
					d.pathBuf.Reset()
				}
				d.readingPath = false
				d.readingKey = false
				d.readingBody = false
				d.valueNext = false
				continue
			}
			if d.highSurrogate != 0 {
				d.appendDecoded(utf8.RuneError)
				d.highSurrogate = 0
			}
			if d.readingKey {
				d.key.WriteRune(r)
			} else {
				d.appendDecoded(r)
			}
			continue
		}
		switch r {
		case '"':
			d.inString = true
			key := strings.ToLower(d.lastKey)
			d.readingBody = d.valueNext && (key == "content" || key == "body" || key == "text" || key == "question" || key == "prompt")
			if d.toolName == "bash" {
				d.readingBody = d.valueNext && (key == "command" || key == "cmd" || key == "arguments")
			}
			if d.toolName == "todo" {
				d.readingBody = d.valueNext && (key == "markdown" || key == "task" || key == "description" || key == "title" || key == "content" || key == "desc" || key == "summary")
				if d.readingBody && key != "markdown" {
					for _, prefix := range "  • " {
						d.appendPlain(prefix)
					}
				}
			}
			if d.edit && d.valueNext {
				switch d.lastKey {
				case "oldText", "old_text", "old_string", "oldString":
					d.readingBody = true
					d.linePrefix = "- "
					d.lineStart = true
				case "newText", "new_text", "new_string", "newString":
					d.readingBody = true
					d.linePrefix = "+ "
					d.lineStart = true
				}
			}
			d.readingPath = d.valueNext && d.toolName != "todo" && (key == "path" || key == "file_path" || key == "filename" || key == "pattern" || key == "query" || key == "url" || key == "name" || key == "task_id")
			if d.toolName == "write" || d.toolName == "edit" {
				d.readingPath = d.valueNext && filetool.IsFilePathKey(key)
			}
			d.readingKey = !d.valueNext
		case ':':
			d.valueNext = true
		case ',':
			d.valueNext = false
			// A later content field represents a different value.
		case '{', '[':
			// A nested object or array consumes its parent's value. Without
			// resetting valueNext, keys inside edit.updates are mistaken for
			// string values and no replacement text is ever previewed.
			d.valueNext = false
			d.lastKey = ""
		}
	}
}

func (d *streamedDraft) appendUnicode(r rune) {
	if d.highSurrogate != 0 {
		high := d.highSurrogate
		d.highSurrogate = 0
		if r >= 0xdc00 && r <= 0xdfff {
			d.appendDecoded(utf16.DecodeRune(high, r))
			return
		}
		d.appendDecoded(utf8.RuneError)
	}
	if r >= 0xd800 && r <= 0xdbff {
		d.highSurrogate = r
		return
	}
	if r >= 0xdc00 && r <= 0xdfff {
		r = utf8.RuneError
	}
	d.appendDecoded(r)
}

func (d *streamedDraft) appendDecoded(r rune) {
	if d.readingKey {
		d.key.WriteRune(r)
		return
	}
	if d.readingBody {
		d.appendVisible(r)
	}
	if d.readingPath && d.pathBuf.Len() < 512 {
		d.pathBuf.WriteRune(r)
		d.path = d.pathBuf.String()
	}
}

func (d *streamedDraft) preview() string { return string(d.tail) }

// livePreview keeps a bounded tail of the generated body, including newlines.
// It is a display value only, not a tool argument or executable source.
func (d *streamedDraft) livePreview() string {
	if d.liveHead == 0 {
		return string(d.liveTail)
	}
	var b strings.Builder
	b.Grow(4096)
	b.WriteString(string(d.liveTail[d.liveHead:]))
	b.WriteString(string(d.liveTail[:d.liveHead]))
	return b.String()
}

// takeDelta emits each printable body character once, even when the JSON
// content value is split at arbitrary SSE chunk boundaries.
func (d *streamedDraft) takeDelta() string {
	value := d.pending.String()
	d.pending.Reset()
	return value
}

// Track JSON completion in linear time. Validating the entire accumulated
// argument string on every token would make large write previews quadratic.
func (s *toolStreamState) appendArguments(fragment string) {
	if !s.payloadKnown && !s.rawTarget {
		if first := strings.TrimSpace(fragment); first != "" {
			s.payloadKnown = true
			s.rawTarget = first[0] != '{' && first[0] != '['
		}
	}
	s.arguments.WriteString(fragment)
	if s.rawTarget {
		for _, r := range fragment {
			s.draft.appendVisible(r)
		}
	} else {
		s.draft.appendJSON(fragment)
	}
	for i := 0; i < len(fragment); i++ {
		c := fragment[i]
		if s.complete {
			if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				s.complete = false
			}
			continue
		}
		if s.inString {
			switch {
			case s.escaped:
				s.escaped = false
			case c == '\\':
				s.escaped = true
			case c == '"':
				s.inString = false
			}
			continue
		}
		if c == '"' {
			s.inString = true
			continue
		}
		switch c {
		case '{':
			s.jsonStarted = true
			s.jsonStack = append(s.jsonStack, '}')
		case '[':
			s.jsonStarted = true
			s.jsonStack = append(s.jsonStack, ']')
		case '}', ']':
			n := len(s.jsonStack)
			if n > 0 && s.jsonStack[n-1] == c {
				s.jsonStack = s.jsonStack[:n-1]
				if len(s.jsonStack) == 0 && s.jsonStarted {
					s.complete = json.Valid([]byte(s.arguments.String()))
				}
			}
		}
	}
}

func newToolStreamSequencer(renderer StreamRenderer, enabled bool) *toolStreamSequencer {
	return &toolStreamSequencer{
		renderer:      renderer,
		enabled:       enabled,
		active:        -1,
		calls:         make(map[int]*toolStreamState),
		sourceIndexes: make(map[int]int),
		unnamed:       make(map[int][]StreamChunk),
		unnamedDraft:  make(map[int]*toolStreamState),
	}
}

// SetLiveContentCallback forwards decoded content, not a truncated snapshot.
// Tool identity is normalized to the execution batch's call index.
func (s *toolStreamSequencer) SetLiveContentCallback(callback func(int, string, string, string)) {
	s.onLiveContent = callback
}

// SetOnReady is called once when a named tool's complete arguments form
// valid JSON. Incomplete arguments cannot be scheduled for execution.
// The caller must still use the normal engine validation and approval path.
func (s *toolStreamSequencer) SetOnReady(callback func(int, string, string)) {
	s.onReady = callback
}

// SetEditDiffPreview supplies the existing read-only diff generator. A complete
// edit call can then display its actual numbered/contextual diff during model
// generation rather than streaming unordered oldText/newText fragments.
func (s *toolStreamSequencer) SetEditDiffPreview(preview func(toolName, arguments string) string) {
	s.editDiffPreview = preview
}

// DeferFollowingCalls keeps later previews out of the terminal until their
// corresponding tool is ready to execute. The first call still streams live.
// This prevents stale completion placeholders when multiple previews scroll
// beyond the terminal's redrawable region.
func (s *toolStreamSequencer) SetBatch(batch *ToolBatch) { s.batch = batch }

func (s *toolStreamSequencer) DeferFollowingCalls() {
	s.deferFollowing = true
}

// PreviewOnly sends all argument deltas to a mutable chat preview. The execution
// engine remains the sole owner of final output and its ordering. This avoids
// printing five complete files before their validation and results exist.
func (s *toolStreamSequencer) PreviewOnly() { s.livePreview = true }

func (s *toolStreamSequencer) progress(index int, state *toolStreamState) {
	if s.onProgress == nil || state == nil {
		return
	}
	preview := ""
	if state.name != "edit" {
		preview = state.draft.livePreview()
	}
	name := state.name
	if path := state.draft.path; path != "" && name != "" {
		name += " " + path
	}
	s.onProgress(index, name, preview, state.arguments.Len())
}

func (s *toolStreamSequencer) Feed(chunk StreamChunk) {
	if !s.enabled || chunk.ToolCallIndex < 0 {
		return
	}
	switch chunk.Type {
	case "tool_name", "tool_target", "tool_call":
	default:
		return
	}

	// Some native providers use content-block indices (which can have gaps),
	// while the execution engine numbers tool calls consecutively.
	sourceIndex := chunk.ToolCallIndex
	index, known := s.sourceIndexes[sourceIndex]
	if !known {
		if chunk.Type != "tool_name" {
			s.unnamed[sourceIndex] = append(s.unnamed[sourceIndex], chunk)
			if chunk.Type == "tool_call" && s.onProgress != nil && (s.livePreview || (s.deferFollowing && (len(s.sourceIndexes) == 0 || (s.calls[0] != nil && s.calls[0].complete)))) {
				draft := s.unnamedDraft[sourceIndex]
				if draft == nil {
					draft = &toolStreamState{}
					s.unnamedDraft[sourceIndex] = draft
				}
				draft.appendArguments(chunk.Content)
				if s.livePreview {
					s.progress(sourceIndex, draft)
				} else {
					s.onProgress(sourceIndex, "", draft.draft.preview(), draft.arguments.Len())
				}
			}
			return
		}
		index = len(s.sourceIndexes)
		s.sourceIndexes[sourceIndex] = index
		delete(s.unnamedDraft, sourceIndex)
	}
	chunk.ToolCallIndex = index
	s.feedIndexed(chunk)
	if chunk.Type == "tool_name" {
		for _, pending := range s.unnamed[sourceIndex] {
			pending.ToolCallIndex = index
			s.feedIndexed(pending)
		}
		delete(s.unnamed, sourceIndex)
	}
}

func (s *toolStreamSequencer) feedIndexed(chunk StreamChunk) {
	index := chunk.ToolCallIndex
	state := s.calls[index]
	if state == nil {
		state = &toolStreamState{}
		s.calls[index] = state
	}
	if s.batch != nil {
		switch chunk.Type {
		case "tool_name":
			s.batch.Observe(index, chunk.Content, "")
		case "tool_call":
			s.batch.Observe(index, "", chunk.Content)
		}
	}
	if chunk.Type == "tool_name" {
		state.name = chunk.Content
		state.draft.toolName = chunk.Content
		state.draft.edit = chunk.Content == "edit"
		// Fallback text calls have no JSON completion marker. Seeing the
		// next opening tag means the previous raw call has ended.
		if s.active >= 0 && index > s.active && s.calls[s.active].rawTarget {
			s.calls[s.active].complete = true
		}
	} else if chunk.Type == "tool_target" {
		state.rawTarget = true
		state.draft.path = chunk.Content
	} else if chunk.Type == "tool_call" {
		state.appendArguments(chunk.Content)
	}
	if s.livePreview {
		s.presentLive(false)
		return
	}

	if s.onProgress != nil && s.deferFollowing && index > 0 && s.calls[0] != nil && s.calls[0].complete {
		preview := ""
		if state.name == "write" {
			preview = state.draft.preview()
		}
		s.onProgress(index, state.name, preview, state.arguments.Len())
	}

	if index == s.active {
		s.render(chunk)
	} else if index > s.active {
		state.events = append(state.events, chunk)
	}
	s.advance(false)
	s.notifyReady(index, state)
}

// Present calls in model order even when argument deltas are interleaved.
// File bodies can follow each other during generation. Other tools keep the
// output tail until execution, so later headings do not precede their results.
func (s *toolStreamSequencer) presentLive(force bool) {
	if s.onLiveContent == nil {
		return
	}
	if s.active < 0 {
		s.active = 0
	}
	for {
		state := s.calls[s.active]
		if state == nil || state.name == "" {
			return
		}
		s.presentLiveCall(s.active, state)
		s.notifyReady(s.active, state)
		if !state.complete && !force {
			return
		}
		if s.deferFollowing && state.name != "write" && state.name != "edit" {
			return
		}
		next := s.calls[s.active+1]
		if next == nil || next.name == "" {
			return
		}
		s.active++
	}
}

// presentLiveCall only displays arguments. Replaying a deferred preview at
// execution time must not invoke the early-write scheduling callback.
func (s *toolStreamSequencer) presentLiveCall(index int, state *toolStreamState) {
	delta := state.draft.takeDelta()
	path := state.draft.path
	if (state.name == "write" || state.name == "edit") && state.draft.readingPath {
		// Wait for the complete filename instead of rewriting a split path.
		path = ""
	}
	if state.name == "edit" && s.editDiffPreview != nil {
		s.onLiveContent(index, state.name, path, "")
		if state.complete && !state.editDiffShown {
			state.editDiffShown = true
			if diff := s.editDiffPreview(state.name, state.arguments.String()); diff != "" {
				s.onLiveContent(index, state.name, path, diff)
			}
		}
	} else {
		s.onLiveContent(index, state.name, path, delta)
	}
	s.progress(index, state)
}

func (s *toolStreamSequencer) notifyReady(index int, state *toolStreamState) {
	if s.onReady == nil || state == nil || state.readyNotified || !state.complete || state.rawTarget || state.name == "" {
		return
	}
	state.readyNotified = true
	s.onReady(index, state.name, state.arguments.String())
}

func (s *toolStreamSequencer) render(chunk StreamChunk) {
	switch chunk.Type {
	case "tool_target":
		s.renderer.SetToolTarget(chunk.Content, chunk.ToolCallIndex)
	case "tool_call":
		s.renderer.WriteToolCall(chunk.Content)
	}
}

func (s *toolStreamSequencer) advance(force bool) {
	for {
		next := s.active + 1
		state := s.calls[next]
		if state == nil || state.name == "" {
			return
		}
		if s.deferFollowing && next > 0 {
			return
		}
		if s.active >= 0 && !force && !s.calls[s.active].complete {
			return
		}

		s.active = next
		s.renderer.StartToolCall(state.name, next)
		for _, event := range state.events {
			s.render(event)
		}
		state.events = nil
		if !state.complete && !force {
			return
		}
	}
}

// Finish displays buffered calls in the legacy streaming mode. In sequential
// display mode, remaining previews are replayed beside their execution result.
func (s *toolStreamSequencer) Finish() {
	if s.livePreview {
		s.presentLive(!s.deferFollowing)
		return
	}
	if s.enabled && !s.livePreview {
		s.advance(true)
	}
}

// ReplayCall guards against mismatched stream indices: an incorrectly paired
// provider preview must not be printed under another call's final result.
func (s *toolStreamSequencer) ReplayCall(index int, call db.ToolCall) {
	state := s.calls[index]
	if state == nil || state.name != call.Function.Name {
		return
	}
	s.Replay(index)
	if s.livePreview {
		if presenter, ok := s.renderer.(interface{ BeginToolExecution(int, string, string) }); ok {
			presenter.BeginToolExecution(index, call.Function.Name, call.Function.Arguments)
		}
	}
}

// Replay displays a later call only when the engine is about to execute it.
// All fragments for that call have already been received from the provider.
// The preview is presentation only: the engine still validates arguments,
// requests approvals, and performs the actual file operation.
func (s *toolStreamSequencer) Replay(index int) {
	if !s.enabled {
		return
	}
	if s.livePreview {
		if state := s.calls[index]; s.onLiveContent != nil && state != nil {
			if index > s.active {
				s.active = index
				s.presentLiveCall(index, state)
				if presenter, ok := s.renderer.(interface{ EndLiveToolContent() }); ok {
					presenter.EndLiveToolContent()
				}
			}
			return
		}
	} else if !s.deferFollowing || index < 1 {
		return
	}
	state := s.calls[index]
	if state == nil || state.name == "" || len(state.events) == 0 {
		return
	}
	s.renderer.StartToolCall(state.name, index)
	for _, event := range state.events {
		s.render(event)
	}
	state.events = nil
	s.renderer.Flush()
}
