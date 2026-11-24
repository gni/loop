package logo

import (
	"image/color"
	"strings"
	"sync"
	"testing"

	"loop/pkg/ui/style"
)

func TestRenderDimensionsAndBounds(t *testing.T) {
	if got := Render(0, 10, 0); got != nil {
		t.Fatalf("expected nil for zero cols, got %d rows", len(got))
	}
	if got := Render(10, 0, 0); got != nil {
		t.Fatalf("expected nil for zero rows, got %d rows", len(got))
	}

	sizes := [][2]int{
		{1, 1},
		{12, 5},
		{40, 12},
		{72, 19},
	}
	for _, sz := range sizes {
		cols, rows := sz[0], sz[1]
		lines := Render(cols, rows, ReducedFrame)
		if len(lines) != rows {
			t.Fatalf("size %dx%d: expected %d rows, got %d", cols, rows, rows, len(lines))
		}
		for r, line := range lines {
			w := style.CellWidth(line)
			if w > cols {
				t.Fatalf("size %dx%d row %d: cell width %d exceeded cols %d", cols, rows, r, w, cols)
			}
			if strings.ContainsRune(line, '\u2014') || strings.ContainsRune(line, '\u2013') {
				t.Fatalf("size %dx%d row %d: unexpected em-dash or en-dash in rendered output", cols, rows, r)
			}
		}
	}
}

func TestVisibleFromFrameZeroAndAllStagesAnimate(t *testing.T) {
	keyframes := []struct {
		name  string
		frame int
	}{
		{"entrance_start", 0},
		{"neural_forward_pass", EntranceFrames + 26},
		{"neural_core_shockwave", ReducedFrame},
		{"neural_backprop_echo", EntranceFrames + 102},
		{"neural_core_convergence", EntranceFrames + 168},
		{"morph_net_code", stageNeuralEnd + 19},
		{"code_prism_arc", morphNetToCode + 34},
		{"morph_code_word", stageCodeEnd + 19},
		{"word_loop_infinity", morphCodeToWord + 45},
		{"morph_word_net", stageWordEnd + 19},
	}
	theme := style.GetTheme("studio")
	app := Appearance{
		Brightness: 55,
		Primary:    theme.Primary,
		Secondary:  theme.Secondary,
		Highlight:  theme.Highlight,
		TextMuted:  theme.TextMuted,
		Background: theme.Background,
	}

	var prevFrame string
	for _, kf := range keyframes {
		lines := Render(72, 19, kf.frame, app)
		lit := 0
		joined := strings.Join(lines, "\n")
		for _, r := range style.StripAnsi(joined) {
			if r >= 0x2801 && r <= 0x28FF {
				lit++
			}
		}
		if lit < 100 {
			t.Fatalf("stage %s (frame %d) rendered too few Braille cells (%d)", kf.name, kf.frame, lit)
		}
		if prevFrame != "" && joined == prevFrame {
			t.Fatalf("stage %s (frame %d) was identical to previous keyframe", kf.name, kf.frame)
		}
		prevFrame = joined
	}
}

func TestElectricityPropagatesThroughNeuralLayers(t *testing.T) {
	app := Appearance{Brightness: 55}
	var stEarly, stMid, stLate, stBack sceneState

	// Early forward pass: Layer 0 (input) should peak first.
	buildSceneState(&stEarly, 144, 76, EntranceFrames+6, app)
	// Mid forward pass: Layer 1 (hidden core) should ignite with active cross-talk & shockwaves.
	buildSceneState(&stMid, 144, 76, EntranceFrames+36, app)
	// Late forward pass: Layer 2 (output) should peak.
	buildSceneState(&stLate, 144, 76, EntranceFrames+66, app)
	// Backprop echo: reverse edges should actively carry Highlight-harmonic bolts.
	buildSceneState(&stBack, 144, 76, EntranceFrames+96, app)

	if stEarly.nodes[0].energy < 0.70 {
		t.Fatalf("expected Layer 0 input neuron to fire brightly early in forward pass, got %f", stEarly.nodes[0].energy)
	}
	if stMid.nodes[4].energy < 0.70 {
		t.Fatalf("expected Layer 1 hidden core neuron to ignite mid forward pass, got %f", stMid.nodes[4].energy)
	}
	if stLate.nodes[7].energy < 0.70 {
		t.Fatalf("expected Layer 2 output neuron to ignite late in forward pass, got %f", stLate.nodes[7].energy)
	}

	activeCross := 0
	for _, ca := range stMid.crossArcs {
		if ca.active {
			activeCross++
		}
	}
	if activeCross == 0 {
		t.Fatalf("expected live vertical inter-hidden cross-talk arcs at mid forward pass")
	}

	reverseActive := 0
	for _, e := range stBack.edges {
		if e.active && e.reverse && e.harmonic > 0.6 {
			reverseActive++
		}
	}
	if reverseActive == 0 {
		t.Fatalf("expected reverse Highlight-harmonic backpropagation bolts during echo sub-phase")
	}

	// Verify Stage 2 code prism air-gap arcs fire across the gap.
	var stCode sceneState
	buildSceneState(&stCode, 144, 76, morphNetToCode+34, app)
	codeArcsActive := 0
	for _, arc := range stCode.codeArcs {
		if arc.active {
			codeArcsActive++
		}
	}
	if codeArcsActive == 0 {
		t.Fatalf("expected live air-gap Tesla arc during Stage 2 code prism")
	}

	// Verify Stage 3 wordmark infinity circuit is active.
	var stWord sceneState
	buildSceneState(&stWord, 144, 76, morphCodeToWord+54, app)
	if stWord.loopCircuitAmp < 0.9 {
		t.Fatalf("expected active figure-eight infinity circuit during Stage 3 wordmark, got %f", stWord.loopCircuitAmp)
	}
}

func TestLightThemeElectricityContrastsAgainstPaper(t *testing.T) {
	theme := style.GetTheme("paper")
	pal := buildPalette(Appearance{
		Brightness: 55,
		Light:      true,
		Primary:    theme.Primary,
		Secondary:  theme.Secondary,
		Highlight:  theme.Highlight,
		TextMuted:  theme.TextMuted,
		Background: theme.Background,
	})
	if !pal.light {
		t.Fatalf("expected paper theme to select light polarity")
	}
	coreCol, sheathCol := electricColors(&pal, 0.0)
	if coreCol.lum() >= pal.bgLum*0.55 {
		t.Fatalf("expected light-theme electric core (%f) to stay deeply darker than paper background (%f)", coreCol.lum(), pal.bgLum)
	}
	if sheathCol.lum() >= pal.bgLum*0.75 {
		t.Fatalf("expected light-theme electric sheath (%f) to contrast cleanly against paper background (%f)", sheathCol.lum(), pal.bgLum)
	}
}

func TestCustomDarkBackgroundOverridesLightFlag(t *testing.T) {
	pal := buildPalette(Appearance{
		Brightness: 55,
		Light:      true,
		Primary:    color.RGBA{216, 220, 226, 255},
		Highlight:  color.RGBA{230, 232, 236, 255},
		Background: color.RGBA{17, 19, 23, 255},
	})
	if pal.light {
		t.Fatalf("expected dark background #111317 to select dark-canvas shading")
	}

	lightPal := buildPalette(Appearance{
		Brightness: 55,
		Light:      true,
		Background: color.RGBA{247, 244, 236, 255},
	})
	if !lightPal.light {
		t.Fatalf("expected paper background #F7F4EC to select light-canvas shading")
	}
}

func TestSeamlessLoopContinuity(t *testing.T) {
	var stEnd, stStart, stPreEnt sceneState
	app := Appearance{Brightness: 55}
	buildSceneState(&stEnd, 144, 76, CycleFrames-1, app)
	buildSceneState(&stStart, 144, 76, EntranceFrames, app)
	buildSceneState(&stPreEnt, 144, 76, EntranceFrames-1, app)

	if dist := stEnd.cam.pos.sub(stStart.cam.pos).length(); dist > 1.0 {
		t.Fatalf("camera jump across loop wrap: dist=%f", dist)
	}
	if dist := stPreEnt.cam.pos.sub(stStart.cam.pos).length(); dist > 0.5 {
		t.Fatalf("camera jump across entrance handoff: dist=%f", dist)
	}
	if got := normalizeFrame(CycleFrames); got != EntranceFrames {
		t.Fatalf("normalizeFrame(CycleFrames) = %d, want %d", got, EntranceFrames)
	}
	if got := normalizeFrame(-5); got != 0 {
		t.Fatalf("normalizeFrame(-5) = %d, want 0", got)
	}
}

func TestRenderDarkAndLightThemes(t *testing.T) {
	themes := []string{"studio", "ashen", "everforest", "amber-crt", "green-crt", "minimal", "light", "paper", "solarized-light"}
	for _, name := range themes {
		theme := style.GetTheme(name)
		lines := Render(72, 19, ReducedFrame, Appearance{
			Brightness: 55,
			Light:      style.IsLightTheme(name),
			Primary:    theme.Primary,
			Secondary:  theme.Secondary,
			Highlight:  theme.Highlight,
			TextMuted:  theme.TextMuted,
			Background: theme.Background,
		})
		litRunes := 0
		for _, line := range lines {
			for _, r := range style.StripAnsi(line) {
				if r >= 0x2801 && r <= 0x28FF {
					litRunes++
				}
			}
		}
		if litRunes < 120 {
			t.Fatalf("theme %q rendered too few Braille cells (%d)", name, litRunes)
		}
	}
}

func TestRenderConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup
	app := Appearance{
		Brightness: 60,
		Primary:    color.RGBA{139, 184, 214, 255},
		Highlight:  color.RGBA{216, 195, 142, 255},
	}
	for g := range 4 {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := range 8 {
				lines := Render(72, 19, EntranceFrames+offset*70+i*22, app)
				if len(lines) != 19 {
					t.Errorf("unexpected row count %d", len(lines))
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkRender72x19(b *testing.B) {
	theme := style.GetTheme("studio")
	app := Appearance{
		Brightness: 55,
		Primary:    theme.Primary,
		Secondary:  theme.Secondary,
		Highlight:  theme.Highlight,
		TextMuted:  theme.TextMuted,
		Background: theme.Background,
	}
	_ = Render(72, 19, ReducedFrame, app)
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		frame := EntranceFrames + (i*7)%(CycleFrames-EntranceFrames)
		_ = Render(72, 19, frame, app)
	}
}
