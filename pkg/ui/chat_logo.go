package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// The logo is a viewport decoration, never a saved chat row or a separate screen.
func (h *ChatHistory) startChatLogo() {
	file, ok := h.reader.W.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	h.logoEnabled = true
	h.logoStop = make(chan struct{})
	h.logoDone = make(chan struct{})
	go func() {
		defer close(h.logoDone)
		ticker := time.NewTicker(60 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-h.logoStop:
				return
			case <-ticker.C:
				TerminalMu.Lock()
				if !h.closed.Load() && !h.captureOnly && h.reader.IsAtMainPrompt.Load() && !h.reader.ActiveTurn.Load() && !h.reader.HasPromptDraft() {
					state := getUI()
					state.StateMu.Lock()
					modal := state.InApprovalPrompt || state.InInteractiveMenu || terminal.Default().InAltScreen()
					state.StateMu.Unlock()
					if !modal {
						rows := h.buffer.View(h.top)
						if h.canShowLogoLocked(rows) {
							h.logoFrame++
							if h.logoFrame >= 240 {
								h.logoFrame = 60
							}
							rows = h.decorateChatLocked(rows)
							render.DrawChatViewport(h.reader.W, rows, h.drawn, h.drawnHint, h.drawnHint)
							h.drawn = rows
						}
					}
				}
				TerminalMu.Unlock()
			}
		}
	}()
}

func (h *ChatHistory) canShowLogoLocked(rows []string) bool {
	if !h.logoEnabled || h.captureOnly || h.browsing || h.buffer.Base() != 0 || h.reader.ActiveTurn.Load() || h.hasOlderLocked() {
		return false
	}
	for _, row := range rows {
		if strings.TrimSpace(style.StripAnsi(row)) != "" {
			return false
		}
	}
	return len(rows) > 0
}

func (h *ChatHistory) decorateChatLocked(rows []string) []string {
	h.logoVisible = h.canShowLogoLocked(rows)
	if !h.logoVisible {
		return rows
	}
	cols, height := min(47, max(1, h.buffer.Width)), min(15, len(rows))
	logo := renderLoopLogo(cols, height, h.logoFrame)
	top, left := (len(rows)-height)/2, max(0, (h.buffer.Width-cols)/2)
	for row, line := range logo {
		rows[top+row] = strings.Repeat(" ", left) + line
	}
	return rows
}

// The entrance plays once; subsequent frames animate the complete word.
// Dense microdots describe smoothly rounded surfaces with normal-based lighting.
func renderLoopLogo(cols, rows, frame int) []string {
	width, height := cols*2, rows*4
	light := make([]float64, width*height)
	depth := make([]float64, len(light))
	for i := range depth {
		depth[i] = math.Inf(1)
	}
	progress := math.Min(1, float64(frame)/30)
	ease := 1 - math.Pow(1-progress, 3)
	scale := math.Min(float64(width-6)/27, float64(height-6)/10)
	scale = math.Max(0.1, scale)
	phase := 2 * math.Pi * float64(frame-60) / 180
	idleBlend := math.Max(0, math.Min(1, float64(frame-30)/30))
	yaw, pitch := idleBlend*0.12*math.Sin(phase), idleBlend*0.055*math.Cos(phase)
	if frame < 30 {
		yaw = -0.65 * (1 - ease)
		pitch = 0.3 * (1 - ease)
	}
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)
	plot := func(x, y, z, brightness float64) {
		x, z = x*cy+z*sy, -x*sy+z*cy
		y, z = y*cp-z*sp, y*sp+z*cp
		perspective := 90 / (90 + z)
		col := int(math.Round(float64(width-1)/2 + x*perspective))
		row := int(math.Round(float64(height-1)/2 + y*perspective))
		if col >= 0 && col < width && row >= 0 && row < height {
			index := row*width + col
			if z < depth[index]-0.001 || (math.Abs(z-depth[index]) < 0.001 && brightness > light[index]) {
				depth[index] = z
				light[index] = brightness
			}
		}
	}
	// Match advances to the narrow l and rounded bowls, rather than fixed slots.
	advances := [4]float64{0, 3, 8.4, 13.8}
	const wordCenter = 9.9
	for glyph, particles := range loopWordParticles {
		reveal := 1.0
		if glyph > 0 {
			reveal = math.Max(0, math.Min(1, float64(frame-30-(glyph-1)*8)/10))
		}
		if reveal == 0 {
			continue
		}
		magnification, center := 1.0, wordCenter
		if glyph == 0 && frame < 30 {
			magnification = 1 + 0.6*(1-ease)
			center = 2 + (wordCenter-2)*ease
		}
		for _, point := range particles {
			x := (advances[glyph] + point.x - center) * scale
			y := (point.y - 4) * scale
			if glyph == 0 && frame < 30 {
				x = ((point.x-2)*magnification + 2 - center) * scale
				y = (point.y-3)*scale*magnification - scale
			}
			if glyph > 0 {
				y -= (1 - reveal) * scale * 1.5
			}
			nx, nz := point.nx*cy+point.nz*sy, -point.nx*sy+point.nz*cy
			ny, nz := point.ny*cp-nz*sp, point.ny*sp+nz*cp
			diffuse := math.Max(0, -0.36*nx-0.48*ny-0.8*nz)
			specular := math.Pow(diffuse, 12)
			sweep := math.Pow((1+math.Cos(phase-(advances[glyph]+point.x)*0.17))/2, 4)
			brightness := (45 + 85*diffuse + 65*specular*sweep) * reveal
			plot(x, y, point.z*scale*magnification, brightness)
		}
	}
	var foreground [24]string
	for i := range foreground {
		shade := 35 + i*165/(len(foreground)-1)
		foreground[i] = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", shade, shade, shade)
	}
	bits := [4][2]rune{{1, 8}, {2, 16}, {4, 32}, {64, 128}}
	lines := make([]string, rows)
	for row := 0; row < rows; row++ {
		var line strings.Builder
		for col := 0; col < cols; col++ {
			var mask rune
			brightness, count := 0.0, 0
			for y := 0; y < 4; y++ {
				for x := 0; x < 2; x++ {

					value := light[(row*4+y)*width+col*2+x]
					if value < 30 {
						continue
					}
					mask |= bits[y][x]
					brightness += value
					count++
				}
			}
			if mask == 0 {
				line.WriteByte(' ')
				continue
			}
			shade := min(len(foreground)-1, max(0, int((brightness/float64(count)-35)*float64(len(foreground)-1)/165)))
			line.WriteString(foreground[shade])
			line.WriteRune(0x2800 + mask)
			line.WriteString("\x1b[0m")
		}
		lines[row] = line.String()
	}
	return lines
}

type logoParticle struct{ x, y, z, nx, ny, nz float64 }

// Cache dense samples of rounded letter surfaces, independent of animation size.
// The distance field preserves smooth bowls and counters instead of bitmap corners.
var loopWordParticles = func() [4][]logoParticle {
	capsule := func(x, y, ax, ay, bx, by float64) float64 {
		dx, dy := bx-ax, by-ay
		t := math.Max(0, math.Min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
		return math.Hypot(x-ax-t*dx, y-ay-t*dy) - 0.46
	}
	distance := func(glyph int, x, y float64) float64 {
		switch glyph {
		case 0:
			return capsule(x, y, 2, 0.2, 2, 6)
		case 1, 2:
			return math.Abs(math.Hypot(x-2.4, y-4.2)-1.9) - 0.46
		default:
			stem := capsule(x, y, 0.6, 2.3, 0.6, 8)
			bowl := math.Abs(math.Hypot(x-2.3, y-4.2)-1.7) - 0.46
			return math.Min(stem, bowl)
		}
	}
	var letters [4][]logoParticle
	for glyph := range letters {
		for iy := 0; iy <= 180; iy++ {
			y := float64(iy)*0.05 - 0.6
			for ix := 0; ix <= 110; ix++ {
				x := float64(ix)*0.05 - 0.2
				d := distance(glyph, x, y)
				if d > 0 {
					continue
				}
				gx := distance(glyph, x+0.001, y) - distance(glyph, x-0.001, y)
				gy := distance(glyph, x, y+0.001) - distance(glyph, x, y-0.001)
				length := math.Hypot(gx, gy)
				radial := math.Max(0, math.Min(1, (0.46+d)/0.46))
				nx, ny := 0.0, 0.0
				if length > 0 {
					nx, ny = radial*gx/length, radial*gy/length
				}
				nz := -math.Sqrt(math.Max(0, 1-radial*radial))
				letters[glyph] = append(letters[glyph], logoParticle{x, y, 0.46 * nz, nx, ny, nz})
			}
		}
	}
	return letters
}()

// Clear the decoration before foreground output reaches the chat writer.
func (h *ChatHistory) clearChatLogoLocked() {
	if !h.logoVisible {
		return
	}
	rows := h.buffer.View(h.top)
	render.DrawChatViewport(h.reader.W, rows, h.drawn, h.drawnHint, h.drawnHint)
	h.drawn = rows
	h.logoVisible = false
}
