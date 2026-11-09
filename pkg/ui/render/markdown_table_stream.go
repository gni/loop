package render

import "strings"

// A possible header and the current row are the only buffered source lines.
// Column widths stay fixed while streaming, so earlier output never moves.
type liveMarkdownTable struct {
	line      strings.Builder
	header    string
	candidate bool
	active    *markdownTable
	newline   bool
}

func (r *LiveMarkdownRenderer) writeTableRune(ch rune, plain *strings.Builder) bool {
	t := &r.table
	if !t.candidate && t.active == nil {
		return false
	}
	if ch != '\n' {
		t.line.WriteRune(ch)
		if t.line.Len() > maxMarkdownTableLineBytes {
			r.flushTable(plain)
		} else if t.header != "" && !strings.ContainsRune(" |:-\t\r", ch) {
			// A non-delimiter cannot confirm the buffered header.
			r.flushTable(plain)
		}
		return true
	}
	line := strings.TrimSuffix(t.line.String(), "\r")
	t.line.Reset()
	if t.active != nil {
		if cells, ok := markdownTableRow(line); ok {
			t.active.writeRow(r.w, cells)
			t.newline, r.endedWithNewline = true, false
			return true
		}
		r.finishTable()
		r.replayMarkdown(line+"\n", plain)
		return true
	}
	if t.header == "" {
		t.header = line
		return true
	}
	if header, align, ok := markdownTableHeader(t.header, line); ok {
		r.beginTable(header, align, true)
		return true
	}
	header := t.header
	r.table = liveMarkdownTable{}
	r.replayMarkdown(header+"\n"+line+"\n", plain)
	return true
}

func (r *LiveMarkdownRenderer) beginTable(header []string, align []tableAlignment, newline bool) {
	r.table = liveMarkdownTable{active: newMarkdownTable(r.w, header, align, r.theme), newline: newline}
	r.endedWithNewline = false
}

func (r *LiveMarkdownRenderer) replayMarkdown(text string, plain *strings.Builder) {
	r.tablePassthrough = true
	defer func() { r.tablePassthrough = false }()
	for _, ch := range text {
		r.writeMarkdownRune(ch, plain)
	}
}

func (r *LiveMarkdownRenderer) finishTable() {
	active, newline := r.table.active, r.table.newline
	r.table = liveMarkdownTable{}
	if active == nil {
		return
	}
	active.finish(r.w)
	r.endedWithNewline = false
	if newline {
		r.emitVisible("\n")
	}
	r.atLineStart = newline
}

func (r *LiveMarkdownRenderer) flushTable(plain *strings.Builder) {
	t := &r.table
	line, header, candidate := t.line.String(), t.header, t.candidate
	t.line.Reset()
	if t.active != nil {
		if line != "" {
			if cells, ok := markdownTableRow(line); ok {
				t.active.writeRow(r.w, cells)
				t.newline = false
				line = ""
			}
		}
		r.finishTable()
		if line != "" {
			r.replayMarkdown(line, plain)
		}
		return
	}
	if candidate {
		r.table = liveMarkdownTable{}
		if header != "" {
			if cells, align, ok := markdownTableHeader(header, line); ok {
				r.beginTable(cells, align, false)
				r.finishTable()
				return
			}
		}
		if header != "" {
			r.replayMarkdown(header+"\n", plain)
		}
		r.replayMarkdown(line, plain)
	}
}
