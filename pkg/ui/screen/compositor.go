package screen

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// Cell stores one terminal grid cell with grapheme text and bounded SGR state.
type Cell struct {
	Text         string
	Width        int
	Style        style.SGRState
	Continuation bool
}

// Compositor implements a reusable, double-buffered, flicker-free terminal screen.
// Frames are composed into the back buffer, diffed against the front buffer, and
// emitted as a single synchronized Mode 2026 write without full-screen clear strobes.
type Compositor struct {
	width  int
	height int
	front  []Cell
	back   []Cell
	out    bytes.Buffer
	dirty  bool
}

// NewCompositor allocates a compositor sized for width x height terminal cells.
func NewCompositor(width, height int) *Compositor {
	c := &Compositor{}
	c.Resize(width, height)
	return c
}

// Resize adjusts the screen dimensions, preserving buffer capacity where possible
// and forcing a clean full diff on the next flush.
func (c *Compositor) Resize(width, height int) {
	width = max(1, width)
	height = max(1, height)
	if c.width == width && c.height == height && len(c.front) == width*height {
		return
	}
	c.width = width
	c.height = height
	total := width * height
	if cap(c.front) >= total {
		c.front = c.front[:total]
	} else {
		c.front = make([]Cell, total)
	}
	if cap(c.back) >= total {
		c.back = c.back[:total]
	} else {
		c.back = make([]Cell, total)
	}
	clear(c.front)
	clear(c.back)
	c.dirty = true
}

// Width returns the current grid width in columns.
func (c *Compositor) Width() int { return c.width }

// Height returns the current grid height in rows.
func (c *Compositor) Height() int { return c.height }

// Invalidate marks the front buffer stale so the next Flush repaints every row.
func (c *Compositor) Invalidate() {
	clear(c.front)
	c.dirty = true
}

// Clear resets the back buffer to empty default cells without allocating.
func (c *Compositor) Clear() {
	blank := Cell{Text: " ", Width: 1}
	for i := range c.back {
		c.back[i] = blank
	}
}

// SetRowANSI parses an ANSI-styled string into 0-based row y of the back buffer,
// clipping cleanly at column Width so lines never auto-wrap and break grid alignment.
func (c *Compositor) SetRowANSI(y int, text string) {
	if y < 0 || y >= c.height {
		return
	}
	rowStart := y * c.width
	blank := Cell{Text: " ", Width: 1}
	for x := 0; x < c.width; x++ {
		c.back[rowStart+x] = blank
	}

	var sgr style.SGRState
	x := 0
	for i := 0; i < len(text) && x < c.width; {
		if text[i] == '\x1b' {
			end := style.SkipAnsiEscape(text, i)
			seq := text[i:end]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				sgr.Apply(seq)
			}
			i = end
			continue
		}
		if text[i] == '\r' || text[i] == '\n' {
			i++
			continue
		}
		if text[i] == '\t' {
			adv := style.TabAdvance(x, style.DefaultTabWidth)
			for k := 0; k < adv && x < c.width; k++ {
				c.back[rowStart+x] = Cell{Text: " ", Width: 1, Style: sgr}
				x++
			}
			i++
			continue
		}
		cluster, w, sz := style.NextGrapheme(text[i:])
		i += sz
		if w == 0 {
			continue
		}
		if x+w > c.width {
			break
		}
		c.back[rowStart+x] = Cell{Text: cluster, Width: w, Style: sgr}
		if w == 2 && x+1 < c.width {
			c.back[rowStart+x+1] = Cell{Continuation: true, Style: sgr}
		}
		x += w
	}
}

// FlushOptions configures cursor placement and optional scroll-region synchronization.
type FlushOptions struct {
	CursorRow    int // 1-based cursor row (0 keeps cursor hidden / unchanged)
	CursorCol    int // 1-based cursor col
	ShowCursor   bool
	ScrollBottom int // If > 0, sets DECSTBM 1..ScrollBottom inside the atomic frame.
}

// Flush diffs the back buffer against the front buffer and writes a single atomic
// Mode 2026 frame to w. Rows are cleared with CSI 2K before writing text so VT100
// pending-wrap (xenl) on full-width rows can never erase the rightmost glyph.
func (c *Compositor) Flush(w io.Writer, opts FlushOptions) error {
	if w == nil || c.width <= 0 || c.height <= 0 {
		return nil
	}

	c.out.Reset()
	changed := false

	beginFrame := func() {
		if changed {
			return
		}
		changed = true
		c.out.WriteString(terminal.BeginSyncOutput)
		c.out.WriteString(terminal.HideCursor)
		if opts.ScrollBottom > 0 {
			fmt.Fprintf(&c.out, "\x1b[1;%dr", opts.ScrollBottom)
		}
	}

	for y := 0; y < c.height; y++ {
		base := y * c.width
		rowChanged := c.dirty
		if !rowChanged {
			for x := 0; x < c.width; x++ {
				if c.back[base+x] != c.front[base+x] {
					rowChanged = true
					break
				}
			}
		}
		if !rowChanged {
			continue
		}

		beginFrame()
		fmt.Fprintf(&c.out, "\x1b[%d;1H\x1b[2K", y+1)

		// Trim trailing default blank cells so narrow rows write minimal bytes.
		lastNonBlank := -1
		for x := c.width - 1; x >= 0; x-- {
			cell := c.back[base+x]
			if cell.Continuation || !cell.Style.IsZero() || (cell.Text != "" && cell.Text != " ") {
				lastNonBlank = x
				break
			}
		}

		var curSGR style.SGRState
		for x := 0; x <= lastNonBlank; x++ {
			cell := c.back[base+x]
			if cell.Continuation {
				continue
			}
			if cell.Style != curSGR {
				if cell.Style.IsZero() {
					c.out.WriteString(terminal.ResetAttributes)
				} else {
					c.out.WriteString(terminal.ResetAttributes)
					c.out.WriteString(cell.Style.Sequence())
				}
				curSGR = cell.Style
			}
			if cell.Text == "" {
				c.out.WriteByte(' ')
			} else {
				c.out.WriteString(cell.Text)
			}
		}
		if !curSGR.IsZero() {
			c.out.WriteString(terminal.ResetAttributes)
		}

		copy(c.front[base:base+c.width], c.back[base:base+c.width])
	}

	c.dirty = false

	if !changed && opts.CursorRow <= 0 {
		return nil
	}
	beginFrame()
	if opts.CursorRow > 0 && opts.CursorCol > 0 {
		fmt.Fprintf(&c.out, "\x1b[%d;%dH", min(c.height, opts.CursorRow), min(c.width, opts.CursorCol))
	}
	if opts.ShowCursor {
		c.out.WriteString(terminal.ShowCursor)
	}
	c.out.WriteString(terminal.EndSyncOutput)
	_, err := w.Write(c.out.Bytes())
	return err
}
