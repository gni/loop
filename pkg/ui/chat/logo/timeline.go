package logo

import (
	"image/color"
	"math"
)

func srgbToLinear(c float64) float64 {
	v := clamp01(c / 255.0)
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearToSRGBByte(v float64) uint8 {
	v = clamp01(v)
	var s float64
	if v <= 0.0031308 {
		s = 12.92 * v
	} else {
		s = 1.055*math.Pow(v, 1.0/2.4) - 0.055
	}
	out := int(math.Round(s * 255.0))
	if out < 0 {
		return 0
	}
	if out > 255 {
		return 255
	}
	return uint8(out)
}

func colorToLinear(c color.Color, fr, fg, fb float64) rgbLinear {
	if c == nil {
		return rgbLinear{srgbToLinear(fr), srgbToLinear(fg), srgbToLinear(fb)}
	}
	r, g, b, _ := c.RGBA()
	return rgbLinear{
		r: srgbToLinear(float64(r >> 8)),
		g: srgbToLinear(float64(g >> 8)),
		b: srgbToLinear(float64(b >> 8)),
	}
}

// boostChroma gently enriches theme hues for high-voltage plasma while keeping
// neutral/monochrome palettes (like "minimal") completely untouched.
func boostChroma(c rgbLinear, amount float64) rgbLinear {
	avg := (c.r + c.g + c.b) / 3.0
	return rgbLinear{
		r: clamp01(avg + (c.r-avg)*amount),
		g: clamp01(avg + (c.g-avg)*amount),
		b: clamp01(avg + (c.b-avg)*amount),
	}
}

func buildPalette(options Appearance) paletteLinear {
	if options.Brightness <= 0 {
		options.Brightness = 55
	}
	if options.Brightness > 100 {
		options.Brightness = 100
	}

	// Determine true canvas polarity from explicit theme background luminance
	// when provided so custom themes with dark backgrounds always shade cleanly.
	effectiveLight := options.Light
	if options.Background != nil {
		bgProbe := colorToLinear(options.Background, 15, 23, 42)
		effectiveLight = bgProbe.lum() > 0.36
	}

	var pri, sec, hi, muted, bg rgbLinear
	if effectiveLight {
		pri = colorToLinear(options.Primary, 38, 114, 174)
		sec = colorToLinear(options.Secondary, 96, 86, 178)
		hi = colorToLinear(options.Highlight, 186, 112, 24)
		muted = colorToLinear(options.TextMuted, 112, 124, 140)
		bg = colorToLinear(options.Background, 247, 244, 236)
	} else {
		pri = colorToLinear(options.Primary, 125, 196, 242)
		sec = colorToLinear(options.Secondary, 148, 168, 214)
		hi = colorToLinear(options.Highlight, 240, 202, 128)
		muted = colorToLinear(options.TextMuted, 118, 130, 150)
		bg = colorToLinear(options.Background, 15, 23, 42)
	}

	// Enrich plasma saturation gently so electric arcs glow vividly in theme colors
	// instead of washing out to flat grey-white, while preserving pure monochrome themes.
	elecPri := boostChroma(pri, 1.48)
	elecHi := boostChroma(hi, 1.48)

	var shellDark, shellMid, coreHot rgbLinear
	if effectiveLight {
		// On light themes, dormant structure is soft graphite ink; peak electric
		// discharge drops decisively into rich deep saturated theme ink.
		shellDark = muted.mix(rgbLinear{0.20, 0.23, 0.28}, 0.35)
		shellMid = muted.mix(bg, 0.22)
		coreHot = elecPri.mix(rgbLinear{0.03, 0.05, 0.10}, 0.58)
	} else {
		// On dark themes, dormant glass stays sleek and dark so traveling
		// high-voltage arcs and firing nuclei ignite with jewel-like drama.
		shellDark = rgbLinear{0.06, 0.08, 0.12}.mix(muted, 0.38)
		shellMid = muted.mix(sec, 0.36).mul(0.76)
		coreHot = rgbLinear{1.0, 0.99, 0.96}.mix(elecHi, 0.22)
	}

	bNorm := float64(options.Brightness) / 100.0
	brightGain := 0.44 + 0.88*math.Pow(bNorm, 0.78)

	return paletteLinear{
		shellDark:  shellDark,
		shellMid:   shellMid,
		primary:    elecPri,
		secondary:  sec,
		highlight:  elecHi,
		coreHot:    coreHot,
		background: bg,
		bgLum:      bg.lum(),
		brightGain: brightGain,
		light:      effectiveLight,
	}
}

// normalizeFrame maps any caller frame cleanly into [0, CycleFrames), wrapping
// steady-state playback seamlessly back to EntranceFrames.
func normalizeFrame(frame int) int {
	if frame < 0 {
		return 0
	}
	if frame < CycleFrames {
		return frame
	}
	loopSpan := CycleFrames - EntranceFrames
	return EntranceFrames + (frame-EntranceFrames)%loopSpan
}

// actionPotential models a crisp biological neural spike: fast electric rise,
// hot peak, and smooth exponential refractory decay.
func actionPotential(dt, riseSigma, decaySigma float64) float64 {
	if dt < -0.35 || dt > 0.65 {
		return 0
	}
	if dt < 0 {
		return math.Exp(-(dt * dt) / (riseSigma * riseSigma))
	}
	return math.Exp(-dt / decaySigma)
}

// buildSceneState choreographs the 3-stage bio-electric animation, 3D orbital
// camera, three-point studio lighting, and active stage electrodynamics.
func buildSceneState(st *sceneState, width, height, frame int, options Appearance) {
	frame = normalizeFrame(frame)
	st.frame = frame
	st.width = width
	st.height = height
	st.pal = buildPalette(options)
	st.timeSec = float64(frame) * FrameInterval.Seconds()

	loopSpan := float64(CycleFrames - EntranceFrames)
	var yaw, pitch, camLift float64
	var neuralActive, codeActive, wordActive float64

	switch {
	case frame < EntranceFrames:
		u := float64(frame) / float64(EntranceFrames)
		s := smootherstep(u)
		st.fromShape, st.toShape = shapeNeural, shapeNeural
		st.morph = 0
		st.morphDir = 1
		st.reveal = 0.48 + 0.52*s
		st.cycleU = 0
		st.stageU = u
		oneMinus := 1.0 - s
		yaw = -0.30 * oneMinus * oneMinus
		pitch = 0.13 * oneMinus * oneMinus
		camLift = 0.25 * oneMinus
		neuralActive = 1.0

	case frame < stageNeuralEnd:
		u := float64(frame-EntranceFrames) / float64(stageNeuralEnd-EntranceFrames)
		st.fromShape, st.toShape = shapeNeural, shapeNeural
		st.morph = 0
		st.morphDir = 1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		yaw = 0.30 * math.Sin(2.0*math.Pi*u)
		pitch = 0.11 * math.Sin(2.0*math.Pi*u)
		camLift = -0.18 * math.Sin(4.0*math.Pi*u)
		neuralActive = 1.0

	case frame < morphNetToCode:
		u := float64(frame-stageNeuralEnd) / float64(morphNetToCode-stageNeuralEnd)
		st.fromShape, st.toShape = shapeNeural, shapeCode
		st.morph = u
		st.morphDir = 1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = 0.38 * arc
		pitch = -0.14 * arc
		camLift = -0.16 * arc
		neuralActive = 1.0 - u
		codeActive = u

	case frame < stageCodeEnd:
		u := float64(frame-morphNetToCode) / float64(stageCodeEnd-morphNetToCode)
		st.fromShape, st.toShape = shapeCode, shapeCode
		st.morph = 0
		st.morphDir = 1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		yaw = 0.36 * math.Sin(2.0*math.Pi*u)
		pitch = 0.12 * math.Sin(2.0*math.Pi*u)
		camLift = -0.15 * math.Sin(2.0*math.Pi*u)
		codeActive = 1.0

	case frame < morphCodeToWord:
		u := float64(frame-stageCodeEnd) / float64(morphCodeToWord-stageCodeEnd)
		st.fromShape, st.toShape = shapeCode, shapeWord
		st.morph = u
		st.morphDir = 1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = -0.34 * arc
		pitch = 0.13 * arc
		camLift = -0.16 * arc
		codeActive = 1.0 - u
		wordActive = u

	case frame < stageWordEnd:
		u := float64(frame-morphCodeToWord) / float64(stageWordEnd-morphCodeToWord)
		st.fromShape, st.toShape = shapeWord, shapeWord
		st.morph = 0
		st.morphDir = 1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		yaw = 0.24 * math.Sin(2.0*math.Pi*u)
		pitch = 0.09 * math.Sin(4.0*math.Pi*u)
		camLift = -0.14 * math.Sin(2.0*math.Pi*u)
		wordActive = 1.0

	default:
		u := float64(frame-stageWordEnd) / float64(CycleFrames-stageWordEnd)
		st.fromShape, st.toShape = shapeWord, shapeNeural
		st.morph = u
		st.morphDir = -1
		st.reveal = 1.0
		st.cycleU = float64(frame-EntranceFrames) / loopSpan
		st.stageU = u
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = 0.36 * arc
		pitch = -0.13 * arc
		camLift = -0.16 * arc
		wordActive = 1.0 - u
		neuralActive = u
	}

	const camDist = 30.5
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)

	camPos := vec3{
		x: -camDist * sy * cp,
		y: camLift + camDist*sp,
		z: -camDist * cy * cp,
	}
	target := vec3{0, camLift * 0.20, 0.0}
	forward := target.sub(camPos).normalize()
	worldUp := vec3{0, 1, 0}
	right := worldUp.cross(forward).normalize()
	up := forward.cross(right).normalize()

	aspectFit := math.Min(float64(width)/27.2, float64(height)/15.4)
	if aspectFit < 0.1 {
		aspectFit = 0.1
	}
	const focalLen = 30.5
	focalPx := focalLen * aspectFit

	st.cam = cameraState{
		pos:        camPos,
		right:      right,
		up:         up,
		forward:    forward,
		focalPx:    focalPx,
		invFocalPx: 1.0 / focalPx,
		focalDist:  camDist - 1.2,
		halfW:      float64(width-1) * 0.5,
		halfH:      float64(height-1) * 0.5,
		scale:      aspectFit,
	}

	lightAngle := 2.0*math.Pi*st.cycleU - 0.55
	st.keyLight = vec3{
		x: -0.44 + 0.26*math.Cos(lightAngle),
		y: -0.58 + 0.14*math.Sin(lightAngle),
		z: -0.66,
	}.normalize()
	st.fillLight = vec3{0.64, 0.32, -0.56}.normalize()
	st.rimLight = vec3{0.28 * math.Sin(lightAngle), -0.40, 0.88}.normalize()

	if st.fromShape == shapeNeural || st.toShape == shapeNeural {
		buildNeuralState(st, frame, neuralActive)
	}
	if st.fromShape == shapeCode || st.toShape == shapeCode {
		buildCodeState(st, codeActive)
	}
	if st.fromShape == shapeWord || st.toShape == shapeWord {
		buildWordState(st, wordActive)
	}
}

// buildNeuralState computes live multi-phase bio-electric electrodynamics across
// the 3 -> 4 -> 3 neural constellation:
//   - Boot ignition [0..24): rapid L0->L1->L2 power-up spark
//   - Sub-phase 1A (Forward Pass): vivid Primary lightning bolts L0 -> L1 (+ vertical cross-talk) -> L2
//   - Sub-phase 1B (Backprop Echo): warm Highlight lightning rebound L2 -> L1 -> L0
//   - Sub-phase 1C (Core Convergence): simultaneous inward surges from L0 & L2 colliding into Hidden Core
func buildNeuralState(st *sceneState, frame int, active float64) {
	var nodeE [numNeuralNodes]float64
	var nodeH [numNeuralNodes]float64
	var nodeShockProg [numNeuralNodes]float64
	var nodeShockAmp [numNeuralNodes]float64

	var edgeHead [numNeuralEdges]float64
	var edgeInt [numNeuralEdges]float64
	var edgeHarm [numNeuralEdges]float64
	var edgeRev [numNeuralEdges]bool
	var edgeAct [numNeuralEdges]bool

	var crossInt [numHiddenBridges]float64
	var crossHarm [numHiddenBridges]float64

	triggerNode := func(idx int, dt, rise, decay, amp, harmonic float64) {
		e := actionPotential(dt, rise, decay) * amp
		if e > nodeE[idx] {
			nodeE[idx] = e
			nodeH[idx] = harmonic
		} else if e > 0.05 {
			nodeE[idx] = math.Min(1.0, nodeE[idx]+e*0.35)
		}
		if dt >= 0.0 && dt < 0.24 {
			sp := dt / 0.24
			sa := (1.0 - sp) * (1.0 - sp) * amp
			if sa > nodeShockAmp[idx] {
				nodeShockProg[idx] = sp
				nodeShockAmp[idx] = sa
			}
		}
	}

	triggerEdge := func(idx int, head, amp, harmonic float64, rev bool) {
		if head < -0.18 || head > 1.38 {
			return
		}
		env := 1.0
		if head < 0 {
			env = clamp01(1.0 + head/0.18)
		} else if head > 1.0 {
			env = clamp01(1.0 - (head-1.0)/0.38)
		}
		v := env * amp
		if v > edgeInt[idx] {
			edgeHead[idx] = head
			edgeInt[idx] = v
			edgeHarm[idx] = harmonic
			edgeRev[idx] = rev
			edgeAct[idx] = true
		}
	}

	if frame < EntranceFrames {
		// Immediate boot cascade from frame 0: Layer 0 flashes at u=0.05,
		// bolts tear to Layer 1 across [0.10..0.62], Layer 1 ignites at u=0.66.
		u := float64(frame) / float64(EntranceFrames)
		for i, sp := range neuralNodes {
			switch sp.layer {
			case 0:
				triggerNode(i, u-(0.04+sp.stagger), 0.08, 0.34, 0.92, 0.0)
			case 1:
				triggerNode(i, u-(0.62+sp.stagger), 0.10, 0.35, 0.98, 0.25)
			default:
				triggerNode(i, u-(0.92+sp.stagger), 0.10, 0.30, 0.45, 0.0)
			}
		}
		for i, e := range neuralEdges {
			if neuralNodes[e.from].layer == 0 {
				head := (u - (0.08 + e.stagger*1.4)) / 0.56
				triggerEdge(i, head, 0.96, 0.0, false)
			}
		}
	} else if st.fromShape == shapeNeural && st.toShape == shapeNeural {
		u := st.stageU // [0..1) across 190 frames (~7.6s)

		// --- Sub-phase 1A: Forward Pass [0.00 .. 0.39] (Primary electric surge) ---
		if u < 0.46 {
			w := u / 0.39 // [0..1.18]
			peaks := [3]float64{0.06, 0.46, 0.86}
			for i, sp := range neuralNodes {
				amp := 0.95
				harm := 0.0
				if sp.layer == 1 {
					amp = 1.0
					harm = 0.22
				}
				triggerNode(i, w-(peaks[sp.layer]+sp.stagger), 0.042, 0.145, amp, harm)
			}
			for i, e := range neuralEdges {
				fromLayer := neuralNodes[e.from].layer
				start := 0.09 + e.stagger*1.15
				dur := 0.35
				harm := 0.0
				if fromLayer == 1 {
					start = 0.49 + e.stagger*1.15
					dur = 0.34
					harm = 0.18
				}
				head := (w - start) / dur
				triggerEdge(i, head, 1.0, harm, false)
			}
			// Vertical inter-hidden cross-talk lightning right as Hidden Core peaks!
			for b := range numHiddenBridges {
				dt := w - (0.48 + float64(b)*0.025)
				cv := actionPotential(dt, 0.032, 0.085)
				if cv > crossInt[b] {
					crossInt[b] = cv * 0.95
					crossHarm[b] = 0.35
				}
			}
		}

		// --- Sub-phase 1B: Backprop Feedback Echo [0.38 .. 0.72] (Highlight surge Right -> Left) ---
		if u >= 0.36 && u < 0.78 {
			w := (u - 0.38) / 0.34
			peaks := [3]float64{0.86, 0.46, 0.06} // Layer 2 -> Layer 1 -> Layer 0
			for i, sp := range neuralNodes {
				triggerNode(i, w-(peaks[sp.layer]+sp.stagger), 0.042, 0.140, 0.96, 0.88)
			}
			for i, e := range neuralEdges {
				fromLayer := neuralNodes[e.from].layer
				// Reverse travel: edges (1->2) run first backwards (2->1), then (0->1) backwards (1->0).
				start := 0.49 + e.stagger*1.15
				dur := 0.34
				if fromLayer == 1 {
					start = 0.09 + e.stagger*1.15
					dur = 0.34
				}
				head := (w - start) / dur
				triggerEdge(i, head, 0.98, 0.88, true)
			}
			for b := range numHiddenBridges {
				dt := w - (0.48 + float64(2-b)*0.024)
				cv := actionPotential(dt, 0.030, 0.080)
				if cv > crossInt[b] {
					crossInt[b] = cv * 0.95
					crossHarm[b] = 0.88
				}
			}
		}

		// --- Sub-phase 1C: Dual-Wave Core Singularity Convergence [0.70 .. 1.00] ---
		if u >= 0.68 {
			w := (u - 0.70) / 0.30 // [0..1]
			for i, sp := range neuralNodes {
				switch sp.layer {
				case 0:
					triggerNode(i, w-(0.05+sp.stagger*0.8), 0.045, 0.15, 0.95, 0.05)
				case 2:
					triggerNode(i, w-(0.05+sp.stagger*0.8), 0.045, 0.15, 0.95, 0.90)
				case 1:
					// Massive central resonance peak feeding into the morph!
					triggerNode(i, w-(0.52+sp.stagger*0.6), 0.055, 0.28, 1.0, 0.55)
				}
			}
			for i, e := range neuralEdges {
				fromLayer := neuralNodes[e.from].layer
				if fromLayer == 0 {
					// Left -> Center forward (Primary)
					head := (w - (0.08 + e.stagger)) / 0.42
					triggerEdge(i, head, 1.0, 0.08, false)
				} else {
					// Right -> Center reverse (Highlight)
					head := (w - (0.08 + e.stagger)) / 0.42
					triggerEdge(i, head, 1.0, 0.92, true)
				}
			}
			// Sustained crackling vertical lattice across all 4 hidden core cells!
			for b := range numHiddenBridges {
				dt := w - (0.50 + float64(b)*0.02)
				cv := actionPotential(dt, 0.045, 0.24)
				if cv > crossInt[b] {
					crossInt[b] = cv * 1.0
					crossHarm[b] = 0.52
				}
			}
		}
	} else {
		// Morphing into or out of Neural: energetic residual glow on cells.
		for i, sp := range neuralNodes {
			e := 0.38
			if sp.layer == 1 {
				e = 0.72
			}
			nodeE[i] = e * active
			nodeH[i] = 0.45
		}
	}

	// Apply live elastic spring expansion and gentle 3D levitation to every cell.
	for i, sp := range neuralNodes {
		e := clamp01(nodeE[i] * active)
		floatY := 0.14 * math.Sin(2.0*math.Pi*st.cycleU*2.0+sp.phase)
		pos := sp.base
		pos.y += floatY
		// Fired cells thrust slightly forward toward the viewer for tactile 3D punch.
		pos.z -= 0.28 * e
		rad := sp.radius * (1.0 + 0.18*e)

		st.nodes[i] = nodePose{
			center:     pos,
			radius:     rad,
			baseRadius: sp.radius,
			energy:     e,
			coreHot:    clamp01((e - 0.32) / 0.68),
			harmonic:   nodeH[i],
			shockProg:  nodeShockProg[i],
			shockAmp:   clamp01(nodeShockAmp[i] * active),
			phase:      sp.phase + st.timeSec*3.4,
		}
	}

	for i, sp := range neuralEdges {
		a := st.nodes[sp.from].center
		b := st.nodes[sp.to].center
		mid := a.add(b).mul(0.5)
		mid.y += sp.archY
		mid.z += sp.archZ

		st.edges[i] = edgePose{
			a:         a,
			ctrl:      mid,
			b:         b,
			head:      edgeHead[i],
			intensity: clamp01(edgeInt[i] * active),
			harmonic:  edgeHarm[i],
			reverse:   edgeRev[i],
			active:    edgeAct[i] && edgeInt[i]*active > 0.03,
		}
	}

	for b := range numHiddenBridges {
		v := clamp01(crossInt[b] * active)
		st.crossArcs[b] = crossArcPose{
			from:      hiddenBridges[b][0],
			to:        hiddenBridges[b][1],
			intensity: v,
			harmonic:  crossHarm[b],
			active:    v > 0.05,
		}
	}
}

// buildCodeState builds the 3D sculpted "</>" prism with two complete high-voltage
// execution surges: '<' ignition -> live air-gap Tesla arc -> '/' flash ->
// live air-gap Tesla arc -> '>' ignition!
func buildCodeState(st *sceneState, active float64) {
	const barR = 0.60
	yFloat := 0.14 * math.Sin(2.0*math.Pi*st.stageU*2.0)

	type barDef struct {
		a, b  vec3
		group int
	}
	defs := [numCodeBars]barDef{
		// '<' left chevron (vertex at left, arms opening toward center)
		{a: vec3{-9.2, 0.0, 1.15}, b: vec3{-4.3, -3.55, 0.25}, group: 0},
		{a: vec3{-9.2, 0.0, 1.15}, b: vec3{-4.3, 3.55, 0.25}, group: 0},
		// '/' central slash arched forward onto focal plane
		{a: vec3{1.90, -4.60, -1.45}, b: vec3{-1.90, 4.60, -1.45}, group: 1},
		// '>' right chevron (arms catching charge and converging into right vertex)
		{a: vec3{4.3, -3.55, 0.25}, b: vec3{9.2, 0.0, 1.15}, group: 2},
		{a: vec3{4.3, 3.55, 0.25}, b: vec3{9.2, 0.0, 1.15}, group: 2},
	}

	// Two energetic left-to-right surges across Stage 2 (1st Primary, 2nd Highlight).
	var w float64
	harmonic := 0.0
	if st.fromShape == shapeCode && st.toShape == shapeCode {
		cycle := st.stageU * 2.0
		if cycle >= 1.0 {
			harmonic = 0.82
		}
		w = math.Mod(cycle, 1.0)
	} else {
		w = 0.50
		harmonic = 0.35
	}

	for i, d := range defs {
		a := d.a
		b := d.b
		a.y += yFloat
		b.y += yFloat

		var chargeT, chargeAmp, baseE float64
		switch d.group {
		case 0:
			// '<' charges from vertex -> tips over w in [0.02..0.32]
			chargeT = (w - 0.02) / 0.30
			if chargeT >= -0.20 && chargeT <= 1.35 {
				chargeAmp = 1.0 * active
			}
			baseE = actionPotential(w-0.16, 0.08, 0.18) * 0.55 * active
		case 1:
			// '/' charges top -> bottom over w in [0.34..0.64]
			chargeT = (w - 0.34) / 0.30
			if chargeT >= -0.20 && chargeT <= 1.35 {
				chargeAmp = 1.0 * active
			}
			baseE = actionPotential(w-0.49, 0.08, 0.20) * 0.70 * active
		default:
			// '>' charges tips -> vertex over w in [0.66..0.96]
			chargeT = (w - 0.66) / 0.30
			if chargeT >= -0.20 && chargeT <= 1.35 {
				chargeAmp = 1.0 * active
			}
			baseE = actionPotential(w-0.80, 0.08, 0.18) * 0.55 * active
		}

		r := barR
		if d.group == 1 {
			r += 0.06
		}
		st.bars[i] = codeBarPose{
			a:          a,
			b:          b,
			radius:     r,
			chargeT:    chargeT,
			chargeAmp:  chargeAmp,
			baseEnergy: baseE,
			harmonic:   harmonic,
			group:      d.group,
		}
	}

	// Air-gap Tesla arc 0: jumps from '<' upper tip to '/' upper body around w in [0.24..0.44].
	arc0Head := (w - 0.24) / 0.18
	arc0Int := actionPotential(w-0.33, 0.045, 0.075) * active
	a0 := st.bars[0].b
	b0 := st.bars[2].a.mix(st.bars[2].b, 0.22)
	c0 := a0.mix(b0, 0.5).add(vec3{0, -0.55, -0.45})
	st.codeArcs[0] = codeAirArc{
		a:         a0,
		b:         b0,
		ctrl:      c0,
		head:      clamp01(arc0Head),
		intensity: arc0Int,
		harmonic:  harmonic,
		active:    arc0Int > 0.05 && st.morph == 0,
	}

	// Air-gap Tesla arc 1: jumps from '/' lower body to '>' lower tip around w in [0.56..0.76].
	arc1Head := (w - 0.56) / 0.18
	arc1Int := actionPotential(w-0.65, 0.045, 0.075) * active
	a1 := st.bars[2].a.mix(st.bars[2].b, 0.78)
	b1 := st.bars[4].a
	c1 := a1.mix(b1, 0.5).add(vec3{0, 0.55, -0.45})
	st.codeArcs[1] = codeAirArc{
		a:         a1,
		b:         b1,
		ctrl:      c1,
		head:      clamp01(arc1Head),
		intensity: arc1Int,
		harmonic:  harmonic,
		active:    arc1Int > 0.05 && st.morph == 0,
	}
}

// buildWordState builds the 3D sculpted "loop" wordmark (true 3D capsules and
// 3D toruses) energized by a living figure-eight infinity current whipping
// through l -> o <-> o -> p!
func buildWordState(st *sceneState, active float64) {
	const tubeR = 0.56
	const shiftX = -0.35
	const shiftY = -0.10
	yFloat := 0.12 * math.Sin(2.0*math.Pi*st.stageU*2.0)

	// Continuous accelerator circuit phase running 2 full passes across Stage 3.
	circuit := 0.35
	if st.fromShape == shapeWord && st.toShape == shapeWord {
		circuit = st.stageU * 2.0
	}
	st.loopCircuitPhase = circuit
	st.loopCircuitAmp = active

	w := math.Mod(circuit, 1.0)
	harm := 0.0
	if circuit >= 1.0 {
		harm = 0.78
	}

	// Energies peak cleanly as the figure-eight current sweeps l -> o1 -> o2 -> p.
	eL := actionPotential(w-0.12, 0.055, 0.14) * active
	eO1 := math.Max(actionPotential(w-0.34, 0.07, 0.22), actionPotential(w-0.62, 0.07, 0.18)) * active
	eO2 := math.Max(actionPotential(w-0.52, 0.07, 0.22), actionPotential(w-0.74, 0.07, 0.18)) * active
	eP := actionPotential(w-0.84, 0.06, 0.16) * active

	// Glyph 0: 'l' vertical bar
	la := vec3{-8.50 + shiftX, -4.20 + shiftY + yFloat, 0.45}
	lb := vec3{-8.50 + shiftX, 2.50 + shiftY + yFloat, 0.45}
	st.word[0] = wordGlyphPose{
		kind:     0,
		a:        la,
		b:        lb,
		minorR:   tubeR,
		energy:   eL,
		coreHot:  clamp01((eL - 0.3) / 0.7),
		harmonic: harm,
		pulseU:   (w - 0.02) / 0.20,
		pulseAmp: active,
	}

	// Glyph 1: first 'o' (3D torus arched forward)
	o1C := vec3{-4.45 + shiftX, 0.40 + shiftY + yFloat, -0.58}
	st.word[1] = wordGlyphPose{
		kind:     1,
		center:   o1C,
		majorR:   2.05,
		minorR:   tubeR,
		energy:   eO1,
		coreHot:  clamp01((eO1 - 0.25) / 0.75),
		harmonic: harm * 0.45,
		pulseU:   w,
		pulseAmp: active,
	}

	// Glyph 2: second 'o' (3D torus arched forward, forming the infinity pair)
	o2C := vec3{1.25 + shiftX, 0.40 + shiftY + yFloat, -0.58}
	st.word[2] = wordGlyphPose{
		kind:     1,
		center:   o2C,
		majorR:   2.05,
		minorR:   tubeR,
		energy:   eO2,
		coreHot:  clamp01((eO2 - 0.25) / 0.75),
		harmonic: 0.35 + 0.55*harm,
		pulseU:   w,
		pulseAmp: active,
	}

	// Glyph 3: 'p' (vertical descender stem + tangent 3D torus bowl)
	pa := vec3{5.35 + shiftX, -1.85 + shiftY + yFloat, 0.42}
	pb := vec3{5.35 + shiftX, 4.55 + shiftY + yFloat, 0.42}
	pBowlC := vec3{7.30 + shiftX, 0.40 + shiftY + yFloat, 0.42}
	st.word[3] = wordGlyphPose{
		kind:     2,
		a:        pa,
		b:        pb,
		center:   pBowlC,
		majorR:   1.95,
		minorR:   tubeR * 0.96,
		energy:   eP,
		coreHot:  clamp01((eP - 0.3) / 0.7),
		harmonic: harm,
		pulseU:   (w - 0.76) / 0.22,
		pulseAmp: active,
	}
}
