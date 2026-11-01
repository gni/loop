package render

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"loop/pkg/ui/style"
)

// HistoryPageSize counts terminal rows, including wrapped tool output.
const HistoryPageSize = 500

// Only row offsets and wrap boundaries remain resident outside the recent page
// and the page being read. The live screen is always writable during browsing.
type scrollArchive struct {
	file    *os.File
	end     int64
	err     error
	mu      sync.Mutex
	refs    int
	warming bool
	warm    map[int64]string
}

func NewPagedScrollback(width, height int) (*Scrollback, error) {
	f, err := os.CreateTemp("", "loop-scrollback-*")
	if err != nil {
		return nil, fmt.Errorf("create chat history cache: %w", err)
	}
	b := NewScrollback(width, height)
	b.archive = &scrollArchive{file: f, refs: 1}
	b.archiveOwned = true
	return b, nil
}

func (b *Scrollback) Close() error {
	if b.archive == nil || !b.archiveOwned {
		return nil
	}
	b.archiveOwned = false
	return b.archive.release()
}

func (a *scrollArchive) release() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refs--
	if a.refs > 0 {
		return nil
	}
	f := a.file
	a.file = nil
	a.warm = nil
	err := f.Close()
	removeErr := os.Remove(f.Name())
	if err != nil {
		return err
	}
	return removeErr
}

func (b *Scrollback) Err() error {
	if b.archive == nil {
		return nil
	}
	b.archive.mu.Lock()
	defer b.archive.mu.Unlock()
	return b.archive.err
}

func (a *scrollArchive) read(offset int64, size int) string {
	a.mu.Lock()
	if text, ok := a.warm[offset]; ok {
		a.mu.Unlock()
		return text
	}
	file := a.file
	a.mu.Unlock()
	if file == nil {
		return "[chat history cache closed]"
	}
	data := make([]byte, size)
	if _, err := file.ReadAt(data, offset); err != nil {
		a.mu.Lock()
		a.err = fmt.Errorf("read chat history cache: %w", err)
		a.mu.Unlock()
		return "[chat history cache unavailable]"
	}
	return string(data)
}

func (r *scrollRow) load() {
	if r.archive != nil && r.size > 0 && r.text == "" {
		r.text = r.archive.read(r.offset, r.size)
	}
}

func (a *scrollArchive) evict(r *scrollRow) {
	r.freeze()
	a.mu.Lock()
	if a.err != nil {
		a.mu.Unlock()
		return
	}
	if r.size == 0 && r.text != "" {
		file, offset := a.file, a.end
		a.end += int64(len(r.text))
		a.mu.Unlock()
		n, err := file.WriteAt([]byte(r.text), offset)
		if err == nil && n != len(r.text) {
			err = io.ErrShortWrite
		}
		if err != nil {
			a.mu.Lock()
			a.err = fmt.Errorf("write chat history cache: %w", err)
			a.mu.Unlock()
			return
		}
		r.archive, r.offset, r.size = a, offset, n
	} else {
		a.mu.Unlock()
	}
	r.text = ""
}

// NewHistoryPage has an independent row collection and shares the append-only
// cache. It can be rendered off the terminal thread, including during shutdown.
func (b *Scrollback) NewHistoryPage() *Scrollback {
	page := NewScrollback(b.Width, 1)
	page.archive = b.archive
	if page.archive != nil {
		page.archive.mu.Lock()
		page.archive.refs++
		page.archive.mu.Unlock()
		page.archiveOwned = true
	}
	return page
}

// SnapshotHistory copies mutable row state; its owner can archive it without
// touching the live document or a layout being restored on the terminal thread.
func (b *Scrollback) SnapshotHistory() *Scrollback {
	p := b.NewHistoryPage()
	p.rows = make([]*scrollRow, len(b.rows))
	for i, r := range b.rows {
		copy := *r
		copy.cells = append([]scrollCell(nil), r.cells...)
		p.rows[i] = &copy
	}
	p.base = max(0, len(p.rows)-1)
	return p
}

func (b *Scrollback) ArchiveHistory(stop <-chan struct{}) error {
	for _, row := range b.rows {
		select {
		case <-stop:
			return io.ErrClosedPipe
		default:
		}
		if b.archive != nil {
			b.archive.evict(row)
		} else {
			row.freeze()
		}
	}
	return b.Err()
}

func (b *Scrollback) FinishTurnLayout() {
	b.FinishHistoryPage()
	if len(b.rows) > 1 && strings.TrimSpace(style.StripAnsi(b.rows[len(b.rows)-1].String())) == "" {
		b.rows = b.rows[:len(b.rows)-1]
	}
	b.base = max(0, len(b.rows)-1)
}

// SwapHistoryRange detaches the previous rows as an alternate layout. Both
// layouts share the append-only disk cache; no formatting happens here.
func (b *Scrollback) SwapHistoryRange(start, end int, page *Scrollback) (old *Scrollback, delta, padding int) {
	start = min(max(0, start), len(b.rows))
	end = min(max(start, end), len(b.rows))
	b.releasePage()
	old = b.NewHistoryPage()
	old.rows = append([]*scrollRow(nil), b.rows[start:end]...)
	old.base = max(0, len(old.rows)-1)
	delta = len(page.rows) - (end - start)
	rows := make([]*scrollRow, 0, len(b.rows)+delta)
	rows = append(rows, b.rows[:start]...)
	rows = append(rows, page.rows...)
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
	return old, delta, padding
}

// InsertPrepared only splices already formatted rows. No message formatting or
// disk reads happen while committing a prefetched page to the live document.
func (b *Scrollback) InsertPrepared(at int, page *Scrollback) int {
	b.releasePage()
	at = min(max(0, at), len(b.rows))
	count := len(page.rows)
	rows := make([]*scrollRow, 0, len(b.rows)+count)
	rows = append(rows, b.rows[:at]...)
	rows = append(rows, page.rows...)
	rows = append(rows, b.rows[at:]...)
	b.rows = rows
	b.base = max(0, len(rows)-b.Height)
	// The page is already on disk. Keep the same resident budget after a splice.
	for i := at; i < at+count; i++ {
		b.rows[i].freeze()
		b.evictRow(i)
	}
	return count
}

func (b *Scrollback) HistoryRowCount() int { return len(b.rows) }

func (b *Scrollback) EmptyHistoryPage() bool {
	return len(b.rows) == 1 && strings.TrimSpace(style.StripAnsi(b.rows[0].String())) == ""
}

func (b *Scrollback) FinishHistoryPage() {
	for len(b.rows) > 1 && strings.TrimSpace(style.StripAnsi(b.rows[len(b.rows)-1].String())) == "" && strings.TrimSpace(style.StripAnsi(b.rows[len(b.rows)-2].String())) == "" {
		b.rows = b.rows[:len(b.rows)-1]
	}
	for _, r := range b.rows {
		r.freeze()
	}
	b.base = max(0, len(b.rows)-b.Height)
}

// Prefetch warms the next disk page on a worker. Foreground painting reads only
// visible rows, rather than synchronously reading all 500 rows at a boundary.
func (b *Scrollback) Prefetch(top, direction int) {
	if b.archive == nil {
		return
	}
	start := max(0, top) / HistoryPageSize * HistoryPageSize
	margin := max(b.Height*4, HistoryPageSize/3)
	if direction < 0 && top-start < margin {
		start -= HistoryPageSize
	}
	if direction > 0 && start+HistoryPageSize-top < margin+b.Height {
		start += HistoryPageSize
	}
	start = max(0, start)
	type span struct {
		offset int64
		size   int
	}
	var spans []span
	bytes := 0
	for i := start; i < min(start+HistoryPageSize, len(b.rows)-max(HistoryPageSize, b.Height)); i++ {
		r := b.rows[i]
		if r.size == 0 {
			continue
		}
		if bytes+r.size > 1<<20 {
			break
		}
		spans = append(spans, span{r.offset, r.size})
		bytes += r.size
	}
	if len(spans) == 0 {
		return
	}
	a := b.archive
	a.mu.Lock()
	if a.warming || a.file == nil {
		a.mu.Unlock()
		return
	}
	if _, ok := a.warm[spans[0].offset]; ok {
		a.mu.Unlock()
		return
	}
	a.warming = true
	a.refs++
	a.mu.Unlock()
	go func() {
		defer a.release()
		cache := make(map[int64]string, len(spans))
		for _, s := range spans {
			cache[s.offset] = a.read(s.offset, s.size)
		}
		a.mu.Lock()
		a.warm = cache
		a.warming = false
		a.mu.Unlock()
	}()
}

func (b *Scrollback) evictRow(i int) {
	if b.archive == nil || i < 0 || i >= len(b.rows)-b.Height || (i >= b.pageStart && i < b.pageEnd) {
		return
	}
	b.archive.evict(b.rows[i])
}

func (b *Scrollback) releasePage() {
	start, end := b.pageStart, b.pageEnd
	b.pageStart, b.pageEnd = -1, 0
	for i := max(0, start); i < end && i < len(b.rows)-HistoryPageSize; i++ {
		b.evictRow(i)
	}
}

func (b *Scrollback) loadPage(top int) {
	if b.archive == nil {
		return
	}
	if top >= len(b.rows)-max(HistoryPageSize, b.Height) {
		b.releasePage()
		return
	}
	start := max(0, top) / HistoryPageSize * HistoryPageSize
	if start == b.pageStart {
		return
	}
	b.releasePage()
	b.pageStart, b.pageEnd = start, min(len(b.rows), max(start+HistoryPageSize, top+b.Height))

}

func (b *Scrollback) trimMemory() {
	if b.archive == nil {
		return
	}
	for i := 0; i < len(b.rows)-max(HistoryPageSize, b.Height); i++ {
		b.evictRow(i)
	}
}

// InsertHistory leaves the live cursor and terminal screen at the tail. It is
// used when an older session page becomes available above the reading position.
func (b *Scrollback) InsertHistory(at int, text string) int {
	delta, _ := b.ReplaceRange(at, at, text)
	return delta
}

// FinishRestore discards the unused screen rows that followed the banner before
// restored history was inserted. Keep the live cursor on the final blank row.
func (b *Scrollback) FinishRestore(end int) int {
	if end <= b.Height {
		return 0
	}
	b.releasePage()
	b.rows = b.rows[:min(end, len(b.rows))]
	// Normal terminal scrolling consumes unused space above the banner. A
	// restore inserts rows directly, so consume that same space explicitly.
	dropped := 0
	for dropped < len(b.rows)-b.Height && strings.TrimSpace(style.StripAnsi(b.rows[dropped].String())) == "" {
		dropped++
	}
	b.rows = b.rows[dropped:]
	b.dropped += dropped
	b.base = len(b.rows) - b.Height
	b.x, b.y, b.wrap = 0, b.Height-1, false
	b.trimMemory()
	return dropped
}
