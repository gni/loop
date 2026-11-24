package scrollback

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

type scrollCell struct {
	r     rune
	extra string
	sgr   style.SGRState
	span  int
}

type scrollRow struct {
	cells        []scrollCell
	text         string
	question     string
	isPromptRule bool
	metaChecked  bool
	dirty, soft  bool
	archive      *scrollArchive
	offset       int64
	size         int
}

func (r *scrollRow) classifyMeta() {
	if r.metaChecked && !r.dirty {
		return
	}
	if r.dirty || r.text == "" {
		return
	}
	plain := strings.TrimRight(style.StripAnsi(r.text), " \t\r\n")
	trimmed := strings.TrimSpace(plain)
	r.isPromptRule = !r.soft && strings.HasPrefix(trimmed, "───") && strings.Count(trimmed, "─") >= 3
	if strings.HasPrefix(plain, "▹ ") {
		q := strings.TrimSpace(strings.TrimPrefix(plain, "▹ "))
		if q != "" {
			r.question = q
		}
	} else if strings.HasPrefix(plain, "> ") {
		q := strings.TrimSpace(strings.TrimPrefix(plain, "> "))
		if q != "" {
			r.question = q
		}
	} else {
		r.question = ""
	}
	r.metaChecked = true
}

// QuestionAnchor identifies the scrollback rows and single-line text of a user question.
type QuestionAnchor struct {
	RuleRow   int // Row of the "─── prompt ───" separator (or PromptRow if none)
	PromptRow int // Row of the "> <question>" line
	Text      string
}

// FirstContentRow returns the index of the first non-blank row in the scrollback
// without performing disk I/O on archived rows.
func (b *Scrollback) FirstContentRow() int {
	if b == nil {
		return 0
	}
	limit := min(b.base, len(b.rows))
	for i := 0; i < limit; i++ {
		r := b.rows[i]
		if r.size > 0 {
			return i
		}
		if r.dirty {
			_ = r.String()
		}
		if r.text != "" && strings.TrimSpace(style.StripAnsi(r.text)) != "" {
			return i
		}
	}
	return 0
}

// QuestionAnchors returns the row index and prompt text of all loaded user questions
// in chronological order without performing disk I/O on archived rows.
func (b *Scrollback) QuestionAnchors() []QuestionAnchor {
	if b == nil {
		return nil
	}
	var out []QuestionAnchor
	for i, r := range b.rows {
		if r.dirty {
			_ = r.String()
		} else if !r.metaChecked && r.text != "" {
			r.classifyMeta()
		}
		if r.question == "" {
			continue
		}
		if i > 0 && b.rows[i-1].soft {
			continue
		}
		isQuestion := strings.HasPrefix(r.question, "!")
		if !isQuestion && i > 0 {
			prev := b.rows[i-1]
			if prev.dirty {
				_ = prev.String()
			} else if !prev.metaChecked && prev.text != "" {
				prev.classifyMeta()
			}
			isQuestion = prev.isPromptRule
		}
		if isQuestion {
			ruleRow := i
			if i > 0 && b.rows[i-1].isPromptRule {
				ruleRow = i - 1
			}
			out = append(out, QuestionAnchor{RuleRow: ruleRow, PromptRow: i, Text: r.question})
		}
	}
	return out
}

func (r *scrollRow) String() string {
	if !r.dirty {
		r.load()
		r.classifyMeta()
		return r.text
	}
	var out strings.Builder
	var active style.SGRState
	for _, c := range r.cells {
		if c.span < 0 {
			continue
		}
		if c.sgr != active {
			if !active.IsZero() {
				out.WriteString(terminal.ResetAttributes)
			}
			if !c.sgr.IsZero() {
				out.WriteString(c.sgr.Sequence())
			}
			active = c.sgr
		}
		if c.r == 0 {
			out.WriteByte(' ')
		} else {
			out.WriteRune(c.r)
		}
		out.WriteString(c.extra)
	}
	if !active.IsZero() {
		out.WriteString(terminal.ResetAttributes)
	}
	r.text, r.dirty = out.String(), false
	r.size = 0
	r.metaChecked = false
	r.classifyMeta()
	return r.text
}

func (r *scrollRow) freeze() {
	if r.cells != nil {
		_ = r.String()
		r.cells = nil
	}
}

// Scrollback records the chat region's actual output, including local commands
// and in-place syntax updates. Archived rows retain compact styled strings;
// only the live screen needs mutable cells. Callers serialize access.
type Scrollback struct {
	dropped        int
	Width, Height  int
	rows           []*scrollRow
	base, x, y     int
	savedX, savedY int
	sgr, savedSGR  style.SGRState
	wrap           bool
	pending        []byte
	archive        *scrollArchive
	archiveOwned   bool
	pageStart      int
	pageEnd        int
	pageSize       int
}

func NewScrollback(width, height int) *Scrollback {
	b := &Scrollback{Width: max(1, width), Height: max(1, height), pageStart: -1, pageSize: HistoryPageSize}
	for range b.Height {
		b.rows = append(b.rows, &scrollRow{})
	}
	return b
}

func (b *Scrollback) MarkdownWidth() int { return b.Width }

func (b *Scrollback) PageSize() int {
	if b != nil && b.pageSize > 0 {
		return b.pageSize
	}
	return HistoryPageSize
}

func (b *Scrollback) SetPageSize(size int) {
	if b != nil && size > 0 {
		b.pageSize = size
	}
}

func (b *Scrollback) Dropped() int { return b.dropped }

func (b *Scrollback) Base() int { return b.base }

func (b *Scrollback) View(top int) []string {
	b.loadPage(top)
	lines := make([]string, b.Height)
	for i := range lines {
		if n := top + i; n >= 0 && n < len(b.rows) {
			lines[i] = b.rows[n].String()
		}
	}
	return lines
}

func (b *Scrollback) up() {
	if len(b.rows) == 0 {
		b.rows = append(b.rows, &scrollRow{})
		b.base = 0
		return
	}
	if b.base == 0 && strings.TrimSpace(style.StripAnsi(b.rows[0].String())) == "" {
		b.dropped++
		copy(b.rows, b.rows[1:])
		b.rows[len(b.rows)-1] = &scrollRow{}
		return
	}
	if b.base < len(b.rows) {
		b.rows[b.base].freeze()
	}
	b.base++
	b.rows = append(b.rows, &scrollRow{})
	b.evictRow(len(b.rows) - b.PageSize() - 1)
}

func (b *Scrollback) lf() {
	if b.y >= b.Height-1 {
		b.up()
		b.y = b.Height - 1
	} else {
		b.y++
	}
	b.x, b.wrap = 0, false
}

func (b *Scrollback) row() *scrollRow {
	if b.y < 0 || b.y >= b.Height {
		return nil
	}
	idx := b.base + b.y
	if idx < 0 || idx >= len(b.rows) {
		return nil
	}
	r := b.rows[idx]
	if r.cells == nil {
		r.load()
	}
	if r.cells == nil && r.text != "" {
		// Thaw a resized archived row only when it becomes writable again.
		line := NewScrollback(b.Width, 1)
		line.Write([]byte(r.text))
		r.cells = line.rows[line.base].cells
		r.size = 0
		r.archive = nil
	}
	return r
}

func (b *Scrollback) put(r rune) {
	if b.y < 0 || b.y >= b.Height {
		return
	}
	w := style.RuneWidth(r)
	if w == 0 {
		line := b.row()
		if line == nil {
			return
		}
		col := min(b.x-1, len(line.cells)-1)
		if b.wrap {
			col = min(b.x, len(line.cells)-1)
		}
		for col >= 0 && line.cells[col].span < 0 {
			col--
		}
		if col >= 0 {
			line.cells[col].extra += string(r)
			line.dirty = true
		}
		return
	}
	if b.wrap || (b.x > 0 && b.x+w > b.Width) {
		if cur := b.row(); cur != nil {
			cur.soft = true
		}
		b.lf()
	}
	line := b.row()
	if line == nil {
		return
	}
	cellSpan := min(w, b.Width)
	for len(line.cells) < b.x+cellSpan {
		line.cells = append(line.cells, scrollCell{})
	}
	// If writing over the continuation cell of an existing wide char, clear its leading half.
	if line.cells[b.x].span < 0 && b.x > 0 {
		line.cells[b.x-1] = scrollCell{}
	}
	// If overwriting the leading half of an existing wide char, clear its continuation cell.
	if line.cells[b.x].span == 2 && b.x+1 < len(line.cells) {
		line.cells[b.x+1] = scrollCell{}
	}
	// If placing a 2-cell glyph at [x, x+1] and x+1 is the leading half of a wide char at [x+1, x+2],
	// clear the orphaned continuation cell at x+2.
	if cellSpan == 2 && b.x+1 < len(line.cells) && line.cells[b.x+1].span == 2 && b.x+2 < len(line.cells) {
		line.cells[b.x+2] = scrollCell{}
	}
	line.cells[b.x] = scrollCell{r: r, sgr: b.sgr, span: cellSpan}
	if cellSpan == 2 && b.x+1 < len(line.cells) {
		line.cells[b.x+1] = scrollCell{span: -1}
	}
	line.dirty = true
	b.x += cellSpan
	if b.x >= b.Width {
		b.x = b.Width - 1
		b.wrap = true
	}
}

func (b *Scrollback) clear(from, to int) {
	line := b.row()
	if line == nil {
		return
	}
	from = max(0, min(from, len(line.cells)))
	to = max(from, min(to, len(line.cells)))
	if from < len(line.cells) && line.cells[from].span < 0 && from > 0 {
		from--
	}
	if to < len(line.cells) && line.cells[to].span < 0 {
		to++
	}
	for i := from; i < to; i++ {
		line.cells[i] = scrollCell{}
	}
	if to == len(line.cells) {
		line.cells = line.cells[:from]
	}
	line.dirty = true
}

func (b *Scrollback) csi(sequence string) {
	final := sequence[len(sequence)-1]
	body := sequence[2 : len(sequence)-1]
	if final == 'm' {
		b.sgr.Apply(sequence)
		return
	}
	if strings.HasPrefix(body, "?") || strings.HasPrefix(body, ">") || strings.HasPrefix(body, "<") {
		return
	}
	var values []int
	for s := range strings.SplitSeq(body, ";") {
		n, _ := strconv.Atoi(s)
		values = append(values, n)
	}
	first := 0
	if len(values) > 0 {
		first = values[0]
	}
	n := max(1, first)
	switch final {
	case 'H', 'f':
		row := max(0, first-1)
		col := 0
		if len(values) > 1 {
			col = max(0, values[1]-1)
		}
		b.y = min(b.Height-1, row)
		b.x = min(b.Width-1, col)
	case 'A':
		b.y = max(0, b.y-n)
	case 'B', 'e':
		b.y = min(b.Height-1, b.y+n)
	case 'C', 'a':
		b.x = min(b.Width-1, b.x+n)
	case 'D':
		b.x = max(0, b.x-n)
	case 'E':
		b.y = min(b.Height-1, b.y+n)
		b.x = 0
	case 'F':
		b.y = max(0, b.y-n)
		b.x = 0
	case 'G', '`':
		b.x = min(b.Width-1, max(0, n-1))
	case 'd':
		b.y = min(b.Height-1, max(0, n-1))
	case 'r':
		b.x, b.y = 0, 0
	case 's':
		b.savedX, b.savedY, b.savedSGR = b.x, b.y, b.sgr
	case 'u':
		b.x = min(b.Width-1, max(0, b.savedX))
		b.y = min(b.Height-1, max(0, b.savedY))
		b.sgr = b.savedSGR
	case 'K':
		switch first {
		case 1:
			b.clear(0, b.x+1)
		case 2:
			b.clear(0, b.Width)
		default:
			b.clear(b.x, b.Width)
		}
	case 'J':
		switch first {
		case 3:
			// Erase saved scrollback lines while keeping the active screen intact.
			if b.base > 0 && b.base <= len(b.rows) {
				b.releasePage()
				b.rows = append([]*scrollRow(nil), b.rows[b.base:]...)
				b.base = 0
			}
		default:
			y := b.y
			for row := 0; row < b.Height && b.base+row < len(b.rows); row++ {
				if first == 2 || (first == 0 && row > y) || (first == 1 && row < y) {
					b.rows[b.base+row] = &scrollRow{}
				}
			}
			if first == 0 {
				b.clear(b.x, b.Width)
			} else if first == 1 {
				b.clear(0, b.x+1)
			}
		}
	case 'S':
		for range min(n, b.Height) {
			b.up()
		}
	case 'T':
		if b.base < len(b.rows) {
			screen := b.rows[b.base:]
			for range min(n, len(screen)) {
				copy(screen[1:], screen[:len(screen)-1])
				screen[0] = &scrollRow{}
			}
		}
	case 'L', 'M':
		if b.y >= 0 && b.y < b.Height && b.base+b.y < len(b.rows) {
			screen := b.rows[b.base+b.y:]
			n = min(n, len(screen))
			if final == 'L' {
				copy(screen[n:], screen[:len(screen)-n])
				for i := 0; i < n; i++ {
					screen[i] = &scrollRow{}
				}
			} else {
				copy(screen, screen[n:])
				for i := len(screen) - n; i < len(screen); i++ {
					screen[i] = &scrollRow{}
				}
			}
		}
	case 'X':
		b.clear(b.x, b.x+n)
	}
	b.wrap = false
}

func (b *Scrollback) Write(data []byte) (int, error) {
	written := len(data)
	if len(b.pending) > 0 {
		data = append(b.pending, data...)
		b.pending = nil
	}
	for i := 0; i < len(data); {
		if data[i] == 27 {
			if i+1 == len(data) {
				b.pending = append(b.pending, data[i:]...)
				break
			}
			end := i + 2
			switch data[i+1] {
			case '[':
				for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
					end++
				}
				if end == len(data) {
					b.pending = append(b.pending, data[i:]...)
					return written, b.Err()
				}
				end++
				b.csi(string(data[i:end]))
			case ']', 'P', '_', '^':
				for end < len(data) && data[end] != 7 && !(data[end] == 27 && end+1 < len(data) && data[end+1] == '\\') {
					end++
				}
				if end == len(data) || (data[end] == 27 && end+1 == len(data)) {
					b.pending = append(b.pending, data[i:]...)
					return written, b.Err()
				}
				if data[end] == 27 {
					end++
				}
				end++
			case '(', ')', '*', '+', '-', '.', '/', '#', '%':
				for end < len(data) && data[end] >= 0x20 && data[end] <= 0x2f {
					end++
				}
				if end == len(data) {
					b.pending = append(b.pending, data[i:]...)
					return written, b.Err()
				}
				end++
			case '7':
				b.savedX, b.savedY, b.savedSGR = b.x, b.y, b.sgr
			case '8':
				b.x = min(b.Width-1, max(0, b.savedX))
				b.y = min(b.Height-1, max(0, b.savedY))
				b.sgr = b.savedSGR
				b.wrap = false
			case 'D':
				// VT100 IND: move down one line, scrolling up at the bottom, preserving column.
				if b.y >= b.Height-1 {
					b.up()
					b.y = b.Height - 1
				} else {
					b.y++
				}
				b.wrap = false
			case 'E':
				// VT100 NEL: move to column 0 of the next line.
				b.lf()
			case 'M':
				// VT100 RI: move up one line, scrolling down at the top, preserving column.
				if b.y > 0 {
					b.y--
				} else if b.base < len(b.rows) {
					screen := b.rows[b.base:]
					copy(screen[1:], screen[:len(screen)-1])
					screen[0] = &scrollRow{}
				}
				b.wrap = false
			}
			i = end
			continue
		}
		if !utf8.FullRune(data[i:]) {
			b.pending = append(b.pending, data[i:]...)
			break
		}
		r, size := utf8.DecodeRune(data[i:])
		i += size
		switch r {
		case '\r':
			b.x = 0
			b.wrap = false
		case '\n', '\v', '\f':
			b.lf()
		case '\b':
			b.x = max(0, b.x-1)
			b.wrap = false
		case '\t':
			end := min(b.Width, b.x+style.TabAdvance(b.x, style.DefaultTabWidth))
			for b.x < end && !b.wrap {
				b.put(' ')
			}
		default:
			if r >= 32 && r != 127 {
				b.put(r)
			}
		}
	}
	return written, b.Err()
}

// Resize reflows soft-wrapped rows once and maps the output cursor and reader's
// anchor to the new width. Height changes preserve the bottom-aligned chat.
func (b *Scrollback) Resize(width, height, outputRow, outputCol, top int, anchors ...*int) (int, int, int) {
	width, height = max(1, width), max(1, height)
	oldAnchor := b.base + max(0, outputRow-1)
	anchor, col := oldAnchor, max(0, outputCol-1)
	if width != b.Width {
		b.releasePage()
		oldRows := b.rows
		mapping := make([]int, len(oldRows)+1)
		var rows []*scrollRow
		for i := 0; i < len(oldRows); {
			base := len(rows)
			var group strings.Builder
			position := wrapCursor{}
			for {
				line := oldRows[i]
				text := line.String()
				plain := style.StripAnsi(text)
				rowStart := position
				if plain != "" {
					firstCluster, firstW, _ := style.NextGrapheme(plain)
					rowStart.prepareCluster(firstCluster, firstW, width)
				}
				// Row anchors refer to their first character. At a wrap boundary
				// that character starts the next row; the output cursor below
				// still retains its distinct pending-wrap position.
				mapping[i] = base + rowStart.row
				if i == oldAnchor {
					cursor := position
					cells := 0
					for j := 0; j < len(plain); {
						cluster, gw, sz := style.NextGrapheme(plain[j:])
						w := wrapClusterWidth(cluster, gw, cells, width)
						if cells+w > col {
							break
						}
						count, _ := cursor.prepareCluster(cluster, gw, width)
						cursor.advanceCluster(cluster, count)
						cells += w
						j += sz
					}
					for remaining := col - cells; remaining > 0; remaining-- {
						count, _ := cursor.prepareCluster(" ", 1, width)
						cursor.advanceCluster(" ", count)
					}
					anchor, col = base+cursor.row, cursor.column
				}
				position.advanceText(plain, width)
				group.WriteString(text)
				// Release resident text of already-archived old rows without writing
				// discarded rows back to disk.
				if line.size > 0 {
					line.text = ""
				}
				i++
				if !line.soft || i == len(oldRows) {
					break
				}
			}
			wrapped := ChatHistoryRows(group.String(), width)
			for j, text := range wrapped {
				nr := &scrollRow{text: text, soft: j < len(wrapped)-1}
				nr.classifyMeta()
				rows = append(rows, nr)
				if b.archive != nil && len(rows) > b.PageSize() {
					b.archive.evict(rows[len(rows)-b.PageSize()-1])
				}
			}
		}
		mapping[len(oldRows)] = len(rows)
		if top >= 0 && top < len(mapping) {
			top = mapping[top]
		}
		for _, a := range anchors {
			if a != nil && *a >= 0 && *a < len(mapping) {
				*a = mapping[*a]
			}
		}
		b.rows = rows
		b.Width = width
		b.base = max(0, len(rows)-b.Height)
	}
	if height < b.Height {
		// Guard against simultaneous widen + shrink where len(b.rows) may be smaller
		// than the old Height. Only advance base or trim blank rows while the active
		// viewport still has more rows than the new height.
		for len(b.rows) > 0 && len(b.rows)-b.base > height {
			if b.base == 0 && strings.TrimSpace(style.StripAnsi(b.rows[0].String())) == "" {
				b.rows = b.rows[1:]
				anchor = max(0, anchor-1)
				top = max(0, top-1)
				for _, a := range anchors {
					if a != nil {
						*a = max(0, *a-1)
					}
				}
			} else if b.base < len(b.rows) {
				b.rows[b.base].freeze()
				b.base++
			} else {
				break
			}
		}
	} else if height > b.Height {
		delta := height - b.Height
		at := min(b.base, len(b.rows))
		padding := make([]*scrollRow, delta)
		for i := range padding {
			padding[i] = &scrollRow{}
		}
		b.rows = append(b.rows, padding...)
		copy(b.rows[at+delta:], b.rows[at:len(b.rows)-delta])
		copy(b.rows[at:], padding)
		if anchor >= at {
			anchor += delta
		}
		if top >= at {
			top += delta
		}
		for _, a := range anchors {
			if a != nil && *a >= at {
				*a += delta
			}
		}
	}
	b.Height = height
	b.base = min(max(0, b.base), len(b.rows))
	for len(b.rows) < b.base+b.Height {
		b.rows = append(b.rows, &scrollRow{})
	}
	for i := 0; i < b.base; i++ {
		b.rows[i].freeze()
	}
	b.trimMemory()
	b.maybeCompactArchive()

	newRow := min(height, max(1, anchor-b.base+1))
	newCol := min(width+1, max(1, col+1))
	b.y = newRow - 1
	if newCol > width {
		b.x = width - 1
		b.wrap = true
	} else {
		b.x = newCol - 1
		b.wrap = false
	}
	b.savedX = min(width-1, max(0, b.savedX))
	b.savedY = min(height-1, max(0, b.savedY))
	return newRow, newCol, max(0, top)
}

// ReplaceRange is used only by an explicit semantic repaint, such as Ctrl+O.
// Local command output after the repainted turn remains in the document.
func (b *Scrollback) ReplaceRange(start, end int, text string) (delta, padding int) {
	start = min(max(0, start), len(b.rows))
	end = min(max(start, end), len(b.rows))
	b.releasePage()
	replacement := NewScrollback(b.Width, 1)
	replacement.archive = b.archive
	replacement.Write([]byte(text))
	delta = len(replacement.rows) - (end - start)
	rows := append([]*scrollRow(nil), b.rows[:start]...)
	rows = append(rows, replacement.rows...)
	rows = append(rows, b.rows[end:]...)
	padding = max(0, b.Height-len(rows))
	if padding > 0 {
		blank := make([]*scrollRow, padding)
		for i := range blank {
			blank[i] = &scrollRow{}
		}
		rows = append(blank, rows...)
	}
	b.rows = rows
	b.base = max(0, len(rows)-b.Height)
	for i := 0; i < b.base; i++ {
		b.rows[i].freeze()
	}
	b.trimMemory()
	b.maybeCompactArchive()
	return delta, padding
}

// FindRow scans archived and live rows without retaining the full transcript.
func (b *Scrollback) FindRow(query string, start, direction int, limits ...int) int {
	query = strings.ToLower(query)
	if query == "" || direction == 0 {
		return -1
	}
	if direction < 0 {
		start = min(start, len(b.rows)-1)
	} else {
		start = max(0, start)
	}
	remaining := len(b.rows)
	if len(limits) > 0 {
		remaining = max(0, limits[0])
	}
	for row := start; row >= 0 && row < len(b.rows) && remaining > 0; row += direction {
		remaining--
		b.loadPage(row)
		text := strings.ToLower(style.StripAnsi(b.rows[row].String()))
		original := len(text)
		if strings.Contains(text, query) {
			return row
		}
		for next, extra := row, 0; next+1 < len(b.rows) && b.rows[next].soft && extra < len(query); next++ {
			b.loadPage(next + 1)
			continuation := strings.ToLower(style.StripAnsi(b.rows[next+1].String()))
			extra += len(continuation)
			text += continuation
			if index := strings.Index(text, query); index >= 0 && index < original {
				return row
			}
		}
	}
	return -1
}

type wrapCursor struct {
	row, column int
}

func wrapClusterWidth(cluster string, graphemeWidth, column, columns int) int {
	if cluster == "\t" {
		return min(style.TabAdvance(column, style.DefaultTabWidth), columns)
	}
	first, _ := utf8.DecodeRuneInString(cluster)
	if unicode.IsControl(first) {
		return 0
	}
	return graphemeWidth
}

func (c *wrapCursor) prepareCluster(cluster string, graphemeWidth, columns int) (cells int, wrapped bool) {
	if cluster == "\n" {
		return 0, false
	}
	cells = wrapClusterWidth(cluster, graphemeWidth, c.column, columns)
	wrapped = c.column > 0 && c.column+cells > columns
	if wrapped {
		c.row++
		c.column = 0
		if cluster == "\t" {
			cells = min(style.TabAdvance(0, style.DefaultTabWidth), columns)
		}
	}
	return cells, wrapped
}

func (c *wrapCursor) advanceCluster(cluster string, cells int) {
	if cluster == "\n" {
		c.row++
		c.column = 0
		return
	}
	first, _ := utf8.DecodeRuneInString(cluster)
	if cluster == "\t" || !unicode.IsControl(first) {
		c.column += cells
	}
}

func (c *wrapCursor) advanceText(text string, columns int) {
	for i := 0; i < len(text); {
		cluster, gw, sz := style.NextGrapheme(text[i:])
		cells, _ := c.prepareCluster(cluster, gw, columns)
		c.advanceCluster(cluster, cells)
		i += sz
	}
}
