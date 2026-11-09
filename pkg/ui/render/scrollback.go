package render

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

type scrollCell struct {
	r          rune
	extra, sgr string
	span       int
}

type scrollRow struct {
	cells       []scrollCell
	text        string
	dirty, soft bool
	archive     *scrollArchive
	offset      int64
	size        int
}

func (r *scrollRow) String() string {
	if !r.dirty {
		r.load()
		return r.text
	}
	var out strings.Builder
	active := ""
	for _, c := range r.cells {
		if c.span < 0 {
			continue
		}
		if c.sgr != active {
			out.WriteString("\x1b[0m")
			out.WriteString(c.sgr)
			active = c.sgr
		}
		if c.r == 0 {
			out.WriteByte(' ')
		} else {
			out.WriteRune(c.r)
		}
		out.WriteString(c.extra)
	}
	out.WriteString("\x1b[0m")
	r.text, r.dirty = out.String(), false
	r.size = 0
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
	sgr, savedSGR  string
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
	if b.base == 0 && strings.TrimSpace(style.StripAnsi(b.rows[0].String())) == "" {
		b.dropped++
		copy(b.rows, b.rows[1:])
		b.rows[len(b.rows)-1] = &scrollRow{}
		return
	}
	b.rows[b.base].freeze()
	b.base++
	b.rows = append(b.rows, &scrollRow{})
	b.evictRow(len(b.rows) - b.PageSize() - 1)
}

func (b *Scrollback) lf() {
	if b.y == b.Height-1 {
		b.up()
	} else {
		b.y++
	}
	b.x, b.wrap = 0, false
}

func (b *Scrollback) row() *scrollRow {
	if b.y < 0 || b.y >= b.Height {
		return nil
	}
	r := b.rows[b.base+b.y]
	if r.cells == nil {
		r.load()
	}
	if r.cells == nil && r.text != "" {
		// Thaw a resized archived row only when it becomes writable again.
		line := NewScrollback(b.Width, 1)
		line.Write([]byte(r.text))
		r.cells = line.rows[line.base].cells
	}
	return r
}

func (b *Scrollback) put(r rune) {
	if b.y < 0 || b.y >= b.Height {
		return
	}
	w := promptRuneWidth(r)
	if w == 0 {
		line := b.row()
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
	if b.wrap || b.x+w > b.Width {
		b.row().soft = true
		b.lf()
	}
	line := b.row()
	for len(line.cells) < b.x+w {
		line.cells = append(line.cells, scrollCell{})
	}
	if line.cells[b.x].span < 0 && b.x > 0 {
		line.cells[b.x-1] = scrollCell{}
	}
	if line.cells[b.x].span == 2 && b.x+1 < len(line.cells) {
		line.cells[b.x+1] = scrollCell{}
	}
	line.cells[b.x] = scrollCell{r: r, sgr: b.sgr, span: w}
	if w == 2 && b.x+1 < b.Width {
		line.cells[b.x+1] = scrollCell{span: -1}
	}
	line.dirty = true
	b.x += w
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
		if body == "" || body == "0" {
			b.sgr = ""
		} else {
			b.sgr += sequence
		}
		return
	}
	if strings.HasPrefix(body, "?") || strings.HasPrefix(body, ">") || strings.HasPrefix(body, "<") {
		return
	}
	var values []int
	for _, s := range strings.Split(body, ";") {
		n, _ := strconv.Atoi(s)
		values = append(values, n)
	}
	n := max(1, values[0])
	switch final {
	case 'H', 'f':
		b.y = max(0, values[0]-1)
		b.x = 0
		if len(values) > 1 {
			b.x = max(0, values[1]-1)
		}
	case 'A':
		b.y = max(0, b.y-n)
	case 'B', 'e':
		b.y += n
	case 'C', 'a':
		b.x = min(b.Width-1, b.x+n)
	case 'D':
		b.x = max(0, b.x-n)
	case 'E':
		b.y += n
		b.x = 0
	case 'F':
		b.y = max(0, b.y-n)
		b.x = 0
	case 'G', '`':
		b.x = max(0, n-1)
	case 'd':
		b.y = max(0, n-1)
	case 'r':
		b.x, b.y = 0, 0
	case 's':
		b.savedX, b.savedY, b.savedSGR = b.x, b.y, b.sgr
	case 'u':
		b.x, b.y, b.sgr = b.savedX, b.savedY, b.savedSGR
	case 'K':
		switch values[0] {
		case 1:
			b.clear(0, b.x+1)
		case 2:
			b.clear(0, b.Width)
		default:
			b.clear(b.x, b.Width)
		}
	case 'J':
		y := b.y
		for row := 0; row < b.Height; row++ {
			if values[0] == 2 || (values[0] == 0 && row > y) || (values[0] == 1 && row < y) {
				b.rows[b.base+row] = &scrollRow{}
			}
		}
		if values[0] == 0 {
			b.clear(b.x, b.Width)
		} else if values[0] == 1 {
			b.clear(0, b.x+1)
		}
	case 'S':
		for range min(n, b.Height) {
			b.up()
		}
	case 'T':
		for range min(n, b.Height) {
			screen := b.rows[b.base:]
			copy(screen[1:], screen[:len(screen)-1])
			screen[0] = &scrollRow{}
		}
	case 'L', 'M':
		if b.y >= 0 && b.y < b.Height {
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
				b.x, b.y, b.sgr = b.savedX, b.savedY, b.savedSGR
				b.wrap = false
			case 'D', 'E':
				b.lf()
			case 'M':
				if b.y > 0 {
					b.y--
				}
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
			end := min(b.Width, ((b.x/8)+1)*8)
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
			start := i
			var group strings.Builder
			prefixes := []string{}
			cursorPrefix := ""
			for {
				line := oldRows[i]
				prefixes = append(prefixes, style.StripAnsi(group.String()))
				if i == oldAnchor {
					plain := style.StripAnsi(line.String())
					var prefix strings.Builder
					cells := 0
					for _, r := range plain {
						w := promptRuneWidth(r)
						if cells+w > col {
							break
						}
						prefix.WriteRune(r)
						cells += w
					}
					cursorPrefix = style.StripAnsi(group.String()) + prefix.String() + strings.Repeat(" ", max(0, col-cells))
				}
				group.WriteString(line.String())
				if b.archive != nil {
					b.archive.evict(line)
				}
				i++
				if !line.soft || i == len(oldRows) {
					break
				}
			}
			base := len(rows)
			for j, prefix := range prefixes {
				layout := LayoutPrompt(prefix, len([]rune(prefix)), width)
				mapping[start+j] = base + layout.CursorRow
			}
			if oldAnchor >= start && oldAnchor < i {
				layout := LayoutPrompt(cursorPrefix, len([]rune(cursorPrefix)), width)
				anchor = base + layout.CursorRow
				col = layout.CursorColumn
			}
			wrapped := ChatHistoryRows(group.String(), width)
			for j, text := range wrapped {
				rows = append(rows, &scrollRow{text: text, soft: j < len(wrapped)-1})
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
			if *a >= 0 && *a < len(mapping) {
				*a = mapping[*a]
			}
		}
		b.rows = rows
		b.Width = width
		b.base = max(0, len(rows)-b.Height)
	}
	if height < b.Height {
		for range b.Height - height {
			if b.base == 0 && strings.TrimSpace(style.StripAnsi(b.rows[0].String())) == "" {
				b.rows = b.rows[1:]
				anchor = max(0, anchor-1)
				top = max(0, top-1)
				for _, a := range anchors {
					*a = max(0, *a-1)
				}
			} else {
				b.rows[b.base].freeze()
				b.base++
			}
		}
	} else if height > b.Height {
		delta := height - b.Height
		at := b.base
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
			if *a >= at {
				*a += delta
			}
		}
	}
	b.Height = height
	for len(b.rows) < b.base+b.Height {
		b.rows = append(b.rows, &scrollRow{})
	}
	for i := 0; i < b.base; i++ {
		b.rows[i].freeze()
	}
	b.trimMemory()
	return min(height, max(1, anchor-b.base+1)), min(width+1, max(1, col+1)), max(0, top)
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
