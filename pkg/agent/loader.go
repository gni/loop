package agent

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"loop/pkg/ui/style"
)

type turnLoader struct {
	agent     *Agent
	w         io.Writer
	theme     style.UITheme
	startTime time.Time
	done      chan struct{}
	stopOnce  sync.Once

	mu         sync.Mutex
	hasDots    bool
	paused     bool
	frameIndex int
	lastFeed   time.Time
}

func (a *Agent) newTurnLoader(ctx context.Context, w io.Writer, theme style.UITheme, startTime time.Time) *turnLoader {
	if a.UI == nil {
		return nil
	}
	if startTime.IsZero() {
		startTime = time.Now()
	}
	tl := &turnLoader{
		agent:     a,
		w:         w,
		theme:     theme,
		startTime: startTime,
		done:      make(chan struct{}),
		hasDots:   true,
		lastFeed:  time.Now(),
	}
	go tl.run(ctx)
	return tl
}

func (tl *turnLoader) currentFrame() string {
	activeDot := style.NewStyle().Foreground(tl.theme.Highlight).Bold(true).Render("•")
	mutedDot := style.NewStyle().Foreground(tl.theme.Border).Render("·")
	frames := []string{
		fmt.Sprintf("%s %s %s", activeDot, mutedDot, mutedDot),
		fmt.Sprintf("%s %s %s", mutedDot, activeDot, mutedDot),
		fmt.Sprintf("%s %s %s", mutedDot, mutedDot, activeDot),
		fmt.Sprintf("%s %s %s", mutedDot, activeDot, mutedDot),
	}
	return frames[tl.frameIndex%len(frames)]
}

func (tl *turnLoader) renderFrame(frame string) {
	select {
	case <-tl.done:
		return
	default:
	}
	elapsed := time.Since(tl.startTime).Seconds()
	timeStr := fmt.Sprintf("(%.1fs)", elapsed)
	timeStyled := style.NewStyle().Foreground(tl.theme.Border).Render(timeStr)
	tl.agent.UI.DrawStatsLine(tl.w, tl.theme, frame, timeStyled)
}

func (tl *turnLoader) run(ctx context.Context) {
	ticker := time.NewTicker(240 * time.Millisecond)
	defer ticker.Stop()

	tl.mu.Lock()
	initFrame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(initFrame)

	for {
		select {
		case <-tl.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			tl.mu.Lock()
			if tl.paused {
				tl.mu.Unlock()
				continue
			}
			// If dots were paused (e.g. streaming) but no chunks received for 500ms, stream has hung/paused -> resume dots
			if !tl.hasDots && time.Since(tl.lastFeed) > 500*time.Millisecond {
				tl.hasDots = true
			}
			frame := ""
			if tl.hasDots {
				frame = tl.currentFrame()
				tl.frameIndex++
			}
			tl.mu.Unlock()

			tl.renderFrame(frame)
		}
	}
}

func (tl *turnLoader) PauseDots() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.hasDots = false
	tl.lastFeed = time.Now()
	tl.mu.Unlock()
	tl.renderFrame("")
}

func (tl *turnLoader) ShowDots() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.hasDots = true
	tl.lastFeed = time.Now()
	frame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(frame)
}

func (tl *turnLoader) Feed() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.lastFeed = time.Now()
	tl.mu.Unlock()
}

func (tl *turnLoader) Pause() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.paused = true
	tl.mu.Unlock()
	tl.agent.UI.DrawStatsLine(tl.w, tl.theme, "", "")
}

func (tl *turnLoader) Resume() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.paused = false
	tl.hasDots = true
	tl.lastFeed = time.Now()
	frame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(frame)
}

func (tl *turnLoader) Stop() {
	if tl == nil {
		return
	}
	tl.stopOnce.Do(func() {
		close(tl.done)
		tl.agent.UI.DrawStatsLine(tl.w, tl.theme, "", "")
	})
}
