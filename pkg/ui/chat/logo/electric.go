package logo

import "math"

// splatAdditiveSubpixel blends volumetric electric plasma radiance into the
// framebuffer with soft depth occlusion against solid 3D glass geometry.
func splatAdditiveSubpixel(c *frameCanvas, sx, sy int, cz float64, col rgbLinear, cov, depthBias float64) {
	if sx < 0 || sx >= c.width || sy < 0 || sy >= c.height || cov <= 0.005 {
		return
	}
	idx := sy*c.width + sx
	if cz > c.depth[idx]+depthBias {
		// Partially occluded behind a solid 3D orb: allow faint translucent refraction bleed.
		if cz > c.depth[idx]+depthBias+2.4 {
			return
		}
		cov *= 0.24
	}
	c.elecCov[idx] = math.Min(1.5, c.elecCov[idx]+cov)
	c.elecR[idx] += col.r * cov
	c.elecG[idx] += col.g * cov
	c.elecB[idx] += col.b * cov
}

// splatElectricSegment2D draws a razor-continuous anti-aliased subpixel plasma
// segment with a blazing inner core and soft electric sheath halo.
func splatElectricSegment2D(c *frameCanvas, x0, y0, z0, x1, y1, z1, coreR, haloR float64, coreCol, sheathCol rgbLinear, intensity, depthBias float64) {
	if intensity <= 0.01 {
		return
	}
	dx, dy := x1-x0, y1-y0
	segLenSq := dx*dx + dy*dy

	pad := haloR + 0.5
	minX := int(math.Floor(math.Min(x0, x1) - pad))
	maxX := int(math.Ceil(math.Max(x0, x1) + pad))
	minY := int(math.Floor(math.Min(y0, y1) - pad))
	maxY := int(math.Ceil(math.Max(y0, y1) + pad))
	if maxX < 0 || minX >= c.width || maxY < 0 || minY >= c.height {
		return
	}
	if minX < 0 {
		minX = 0
	}
	if maxX >= c.width {
		maxX = c.width - 1
	}
	if minY < 0 {
		minY = 0
	}
	if maxY >= c.height {
		maxY = c.height - 1
	}

	haloSq := haloR * haloR
	invHalo := 1.0 / math.Max(0.25, haloR)
	for sy := minY; sy <= maxY; sy++ {
		py := float64(sy) - y0
		for sx := minX; sx <= maxX; sx++ {
			px := float64(sx) - x0
			var h float64
			if segLenSq > 1e-6 {
				h = clamp01((px*dx + py*dy) / segLenSq)
			}
			rx := px - dx*h
			ry := py - dy*h
			dSq := rx*rx + ry*ry
			if dSq > haloSq {
				continue
			}
			dist := math.Sqrt(dSq)
			cz := z0 + (z1-z0)*h

			if dist <= coreR {
				coreMix := 1.0 - (dist/math.Max(0.2, coreR))*0.35
				col := sheathCol.mix(coreCol, coreMix)
				splatAdditiveSubpixel(c, sx, sy, cz, col, intensity*1.15, depthBias)
			} else {
				falloff := clamp01(1.0 - (dist-coreR)*invHalo)
				falloff = falloff * falloff
				splatAdditiveSubpixel(c, sx, sy, cz, sheathCol, intensity*falloff*0.62, depthBias)
			}
		}
	}
}

func electricColors(pal *paletteLinear, harmonic float64) (coreCol, sheathCol rgbLinear) {
	sheathCol = pal.primary.mix(pal.highlight, clamp01(harmonic))
	if pal.light {
		// On light themes, core burns as razor-deep vivid theme ink against paper.
		coreCol = sheathCol.mul(0.46).mix(pal.coreHot, 0.36)
		sheathCol = sheathCol.mul(0.74)
		return coreCol, sheathCol
	}
	// On dark themes, preserve rich jewel-toned theme hue across the plasma sheath
	// while focusing incandescent brilliance tightly in the inner filament core.
	coreCol = sheathCol.mix(pal.coreHot, 0.34).mul(1.75)
	sheathCol = sheathCol.mul(1.38)
	return coreCol, sheathCol
}

// drawElectricBolt3D renders a crackling, razor-sharp 3D fractal Tesla lightning
// bolt racing along a quadratic Bezier path with dynamic jagged perpendicular
// jitter, blazing traveling plasma spearhead, delicate forked micro-branches,
// and shedding 3D micro-sparks.
func drawElectricBolt3D(
	st *sceneState,
	c *frameCanvas,
	a, ctrl, b vec3,
	headT, intensity, harmonic float64,
	reverse bool,
	seed uint32,
	isAirGap bool,
) {
	if intensity <= 0.03 {
		return
	}
	if reverse {
		a, b = b, a
	}

	cam := &st.cam
	coreCol, sheathCol := electricColors(&st.pal, harmonic)

	const steps = 20
	timeTick := uint32(st.frame)
	jitterAmp := 0.44
	if isAirGap {
		jitterAmp = 0.60
	}

	// Build local orthonormal ribbon frame along chord a -> b.
	chord := b.sub(a).normalize()
	ref := vec3{0, 0, 1}
	if math.Abs(chord.dot(ref)) > 0.85 {
		ref = vec3{0, 1, 0}
	}
	perpU := chord.cross(ref).normalize()
	perpV := chord.cross(perpU).normalize()

	var pts [steps + 1]vec3
	var localE [steps + 1]float64

	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		basePt := evalQuadBezier(a, ctrl, b, t)

		// Compute sharp traveling packet envelope (steep leading edge, sizzling tail).
		var segE float64
		if isAirGap {
			segE = intensity * (0.58 + 0.42*math.Exp(-math.Pow(t-headT, 2)/0.16))
		} else {
			behind := headT - t
			if behind >= 0 {
				segE = math.Exp(-(behind*behind)/0.044) * intensity
			} else {
				segE = math.Exp(-(behind*behind)/0.0045) * intensity
			}
		}
		localE[s] = clamp01(segE)

		// Pin bolt endpoints cleanly into cell/prism anchors while letting the
		// active high-voltage span crackle with authentic jagged lightning vertices!
		pin := math.Sin(math.Pi * t)
		if pin > 0.01 && segE > 0.08 {
			s1 := hashSigned(seed ^ (uint32(s) * 977) ^ (timeTick * 1315423911))
			s2 := hashSigned((seed + 19) ^ (uint32(s) * 1993) ^ (timeTick * 2654435761))
			zig := math.Sin(t*19.0 + float64(st.frame)*2.1 + float64(seed&15))
			amp := jitterAmp * pin * (0.28 + 0.72*segE)
			pts[s] = basePt.
				add(perpU.mul((s1*0.76 + zig*0.24) * amp)).
				add(perpV.mul(s2 * amp * 0.65))
		} else {
			pts[s] = basePt
		}
	}

	// Render razor-crisp continuous lightning filament (slender core, delicate sheath).
	for s := 0; s < steps; s++ {
		e := math.Max(localE[s], localE[s+1])
		if e < 0.08 {
			continue
		}
		x0, y0, z0, ok0 := projectWorld(cam, pts[s])
		x1, y1, z1, ok1 := projectWorld(cam, pts[s+1])
		if !ok0 || !ok1 {
			continue
		}
		coreR := 0.36 + 0.26*e
		haloR := 0.68 + 0.56*e
		splatElectricSegment2D(c, x0, y0, z0, x1, y1, z1, coreR, haloR, coreCol, sheathCol, e*st.reveal, 0.95)
	}

	// Compact blazing plasma spearhead racing along the axon!
	if !isAirGap && headT >= 0.02 && headT <= 0.98 {
		headIdx := int(math.Round(headT * float64(steps)))
		if headIdx < 0 {
			headIdx = 0
		} else if headIdx > steps {
			headIdx = steps
		}
		hp := pts[headIdx]
		if hx, hy, hz, ok := projectWorld(cam, hp); ok {
			splatElectricSegment2D(c, hx, hy, hz, hx+0.01, hy+0.01, hz, 0.72, 1.45, coreCol, sheathCol, intensity*1.25*st.reveal, 1.2)
		}
	}

	// Delicate razor-thin forked lightning tendrils snapping off peak voltage zones!
	if intensity > 0.48 {
		const maxForks = 2
		for f := range maxForks {
			fSeed := seed ^ uint32(f*4099+73) ^ (timeTick * 1597334677)
			if hashUnit(fSeed) > 0.48*intensity {
				continue
			}
			anchorT := headT - 0.05 - 0.14*hashUnit(fSeed+11)
			if isAirGap {
				anchorT = 0.22 + 0.56*hashUnit(fSeed+11)
			}
			if anchorT <= 0.10 || anchorT >= 0.90 {
				continue
			}
			aIdx := int(anchorT * float64(steps))
			if aIdx < 1 || aIdx >= steps {
				continue
			}
			startPt := pts[aIdx]
			dirU := hashSigned(fSeed + 23)
			dirV := hashSigned(fSeed + 37)
			forkLen := (0.65 + 0.70*intensity) * (0.7 + 0.55*hashUnit(fSeed+51))
			midPt := startPt.
				add(chord.mul(forkLen * 0.35)).
				add(perpU.mul(dirU * forkLen * 0.62)).
				add(perpV.mul(dirV * forkLen * 0.42))
			tipPt := midPt.
				add(chord.mul(forkLen * 0.28)).
				add(perpU.mul((dirU + 0.65*hashSigned(fSeed+71)) * forkLen * 0.48)).
				add(perpV.mul((dirV + 0.65*hashSigned(fSeed+89)) * forkLen * 0.38))

			x0, y0, z0, ok0 := projectWorld(cam, startPt)
			x1, y1, z1, ok1 := projectWorld(cam, midPt)
			x2, y2, z2, ok2 := projectWorld(cam, tipPt)
			if ok0 && ok1 {
				splatElectricSegment2D(c, x0, y0, z0, x1, y1, z1, 0.28, 0.55, coreCol, sheathCol, intensity*0.80*st.reveal, 0.90)
			}
			if ok1 && ok2 {
				splatElectricSegment2D(c, x1, y1, z1, x2, y2, z2, 0.22, 0.42, coreCol, sheathCol, intensity*0.50*st.reveal, 0.90)
			}
		}
	}

	// Shedding 3D pinpoint micro-sparks spraying cleanly into open negative space!
	if intensity > 0.35 && headT > 0.05 && headT < 1.15 {
		const sparkCount = 5
		for k := range sparkCount {
			sKey := seed ^ uint32(k*8191+313)
			age := math.Mod(headT*3.4+hashUnit(sKey), 1.0)
			spawnT := clamp01(headT - age*0.24)
			if spawnT < 0.05 || spawnT > 0.95 {
				continue
			}
			origin := evalQuadBezier(a, ctrl, b, spawnT)
			vx := hashSigned(sKey + 17)
			vy := hashSigned(sKey + 43)
			drift := 0.35 + age*1.35
			spPt := origin.add(perpU.mul(vx * drift)).add(perpV.mul(vy * drift))
			if sx, sy, sz, ok := projectWorld(cam, spPt); ok {
				fade := (1.0 - age) * (1.0 - age) * intensity * st.reveal
				splatAdditiveSubpixel(c, int(math.Round(sx)), int(math.Round(sy)), sz, coreCol, fade*0.95, 1.0)
			}
		}
	}
}

// drawShockwaveRing3D projects an expanding tilted 3D electromagnetic shockwave
// ring around an ignited neural cell, breaking up into pinpoint outer sparks.
func drawShockwaveRing3D(st *sceneState, c *frameCanvas, center vec3, baseR, prog, amp, harmonic float64, seed uint32) {
	if amp <= 0.04 || prog <= 0 || prog >= 1 {
		return
	}
	cam := &st.cam
	coreCol, sheathCol := electricColors(&st.pal, harmonic)

	ringR := baseR * (1.12 + 1.48*math.Pow(prog, 0.72))
	tilt := 0.58 + 0.42*hashSigned(seed)
	spin := st.timeSec*1.9 + float64(seed&7)

	uAxis := vec3{math.Cos(spin), math.Sin(spin) * math.Cos(tilt), math.Sin(spin) * math.Sin(tilt)}.normalize()
	vAxis := vec3{-math.Sin(spin), math.Cos(spin) * math.Cos(tilt), math.Cos(spin) * math.Sin(tilt)}.normalize()

	const segments = 28
	var prevX, prevY, prevZ float64
	prevOK := false
	for i := 0; i <= segments; i++ {
		ang := 2.0 * math.Pi * float64(i) / float64(segments)
		ripple := 1.0 + 0.05*math.Sin(ang*5.0+st.timeSec*12.0+float64(seed&3))
		pt := center.
			add(uAxis.mul(math.Cos(ang) * ringR * ripple)).
			add(vAxis.mul(math.Sin(ang) * ringR * ripple))
		sx, sy, sz, ok := projectWorld(cam, pt)
		if i > 0 && prevOK && ok {
			if prog < 0.60 {
				// Razor-crisp 1-dot 3D orbital shockwave ring!
				splatElectricSegment2D(c, prevX, prevY, prevZ, sx, sy, sz, 0.28, 0.58, coreCol, sheathCol, amp*0.88*st.reveal, 0.35)
			} else {
				if hashUnit(seed^uint32(i*37+st.frame)) > prog*0.58 {
					splatAdditiveSubpixel(c, int(math.Round(sx)), int(math.Round(sy)), sz, coreCol, amp*1.1*st.reveal, 0.35)
				}
			}
		}
		prevX, prevY, prevZ, prevOK = sx, sy, sz, ok
	}
}

func renderNeuralElectricity(st *sceneState, c *frameCanvas) {
	// 1. Expanding 3D electromagnetic shockwave rings around firing cells.
	for i := range st.nodes {
		n := &st.nodes[i]
		if n.shockAmp > 0.04 {
			drawShockwaveRing3D(st, c, n.center, n.baseRadius, n.shockProg, n.shockAmp, n.harmonic, uint32(i*997+13))
		}
	}

	// 2. Crackling high-voltage synaptic lightning bolts along all active pathways!
	for i := range st.edges {
		e := &st.edges[i]
		if !e.active {
			continue
		}
		drawElectricBolt3D(st, c, e.a, e.ctrl, e.b, e.head, e.intensity, e.harmonic, e.reverse, uint32(i*3571+101), false)
	}

	// 3. Live vertical inter-hidden cross-talk arcs crackling between central core cells!
	for b := range st.crossArcs {
		ca := &st.crossArcs[b]
		if !ca.active {
			continue
		}
		a := st.nodes[ca.from].center
		bPt := st.nodes[ca.to].center
		mid := a.mix(bPt, 0.5).add(vec3{0.45 * math.Sin(st.timeSec*9.0+float64(b)), 0, -0.55})
		drawElectricBolt3D(st, c, a, mid, bPt, 0.5, ca.intensity, ca.harmonic, false, uint32(b*7919+503), true)
	}
}

func renderCodeElectricity(st *sceneState, c *frameCanvas) {
	// Traveling live surface-hugging bolts along '<', '/', '>'.
	for i := range st.bars {
		bar := &st.bars[i]
		if bar.chargeAmp <= 0.05 || bar.chargeT < -0.15 || bar.chargeT > 1.30 {
			continue
		}
		mid := bar.a.mix(bar.b, 0.5).add(vec3{0, 0, -0.35})
		drawElectricBolt3D(st, c, bar.a, mid, bar.b, bar.chargeT, bar.chargeAmp*0.92, bar.harmonic, false, uint32(i*6151+41), false)
	}

	// Crackling 3D air-gap Tesla arcs leaping across '< -> /' and '/ -> >'!
	for i := range st.codeArcs {
		arc := &st.codeArcs[i]
		if !arc.active {
			continue
		}
		drawElectricBolt3D(st, c, arc.a, arc.ctrl, arc.b, arc.head, arc.intensity*1.12, arc.harmonic, false, uint32(i*12289+881), true)
	}
}

func renderWordElectricity(st *sceneState, c *frameCanvas) {
	if st.loopCircuitAmp <= 0.04 {
		return
	}
	cam := &st.cam
	amp := st.loopCircuitAmp

	// 1. Vertical surge bolt along 'l' stem and 'p' stem + air-gap jumps l->o1 and o2->p!
	lGlyph := &st.word[0]
	o1 := &st.word[1]
	o2 := &st.word[2]
	pGlyph := &st.word[3]
	if lGlyph.pulseU >= -0.12 && lGlyph.pulseU <= 1.25 {
		mid := lGlyph.a.mix(lGlyph.b, 0.5).add(vec3{0, 0, -0.40})
		drawElectricBolt3D(st, c, lGlyph.a, mid, lGlyph.b, lGlyph.pulseU, amp*0.95, lGlyph.harmonic, false, 1009, false)
	}
	if pGlyph.pulseU >= -0.12 && pGlyph.pulseU <= 1.25 {
		mid := pGlyph.a.mix(pGlyph.b, 0.5).add(vec3{0, 0, -0.40})
		drawElectricBolt3D(st, c, pGlyph.a, mid, pGlyph.b, pGlyph.pulseU, amp*0.95, pGlyph.harmonic, false, 2027, false)
	}

	wCycle := math.Mod(st.loopCircuitPhase, 1.0)
	// Crisp air-gap spark jumping from 'l' into first 'o' around wCycle=0.22.
	if jumpL := actionPotential(wCycle-0.21, 0.04, 0.07) * amp; jumpL > 0.08 {
		aPt := lGlyph.a.mix(lGlyph.b, 0.65)
		bPt := vec3{o1.center.x - o1.majorR, o1.center.y, o1.center.z - 0.30}
		cPt := aPt.mix(bPt, 0.5).add(vec3{0, -0.45, -0.40})
		drawElectricBolt3D(st, c, aPt, cPt, bPt, 0.5, jumpL*1.08, lGlyph.harmonic, false, 3109, true)
	}
	// Crisp air-gap spark jumping from second 'o' into 'p' around wCycle=0.74.
	if jumpP := actionPotential(wCycle-0.74, 0.04, 0.07) * amp; jumpP > 0.08 {
		aPt := vec3{o2.center.x + o2.majorR, o2.center.y, o2.center.z - 0.30}
		bPt := pGlyph.a.mix(pGlyph.b, 0.35)
		cPt := aPt.mix(bPt, 0.5).add(vec3{0, -0.45, -0.40})
		drawElectricBolt3D(st, c, aPt, cPt, bPt, 0.5, jumpP*1.08, pGlyph.harmonic, false, 3779, true)
	}

	// 2. Figure-eight (infinity) high-voltage plasma accelerator whipping around
	//    the two central 'o' toruses with blazing head, crackling wake, and
	//    live crossover arc bridging the twin rings!
	infPhase := st.loopCircuitPhase * 2.0 * math.Pi

	drawTorusBolt := func(g *wordGlyphPose, headAng, dir, harmonic float64, seed uint32) {
		coreCol, sheathCol := electricColors(&st.pal, harmonic)
		const trailSteps = 16
		var prevX, prevY, prevZ float64
		prevOK := false
		for s := 0; s <= trailSteps; s++ {
			frac := float64(s) / float64(trailSteps) // 0 = blazing head, 1 = tail end
			ang := headAng - dir*frac*2.15
			jitter := 0.28 * math.Sin(math.Pi*frac) * hashSigned(seed^uint32(s*173+st.frame*97))
			rad := g.majorR + 0.14 + jitter
			zOff := -g.minorR*1.05 + 0.16*hashSigned(seed^uint32(s*311+st.frame*53))
			pt := vec3{
				x: g.center.x + rad*math.Cos(ang),
				y: g.center.y + rad*math.Sin(ang),
				z: g.center.z + zOff,
			}
			sx, sy, sz, ok := projectWorld(cam, pt)
			if s > 0 && prevOK && ok {
				e := math.Pow(1.0-frac, 1.30) * amp
				coreR := 0.36 + 0.34*(1.0-frac)
				haloR := 0.72 + 0.64*(1.0-frac)
				splatElectricSegment2D(c, prevX, prevY, prevZ, sx, sy, sz, coreR, haloR, coreCol, sheathCol, e*st.reveal, 1.1)
			}
			prevX, prevY, prevZ, prevOK = sx, sy, sz, ok
		}
		headPt := vec3{
			x: g.center.x + (g.majorR+0.14)*math.Cos(headAng),
			y: g.center.y + (g.majorR+0.14)*math.Sin(headAng),
			z: g.center.z - g.minorR*1.05,
		}
		if hx, hy, hz, ok := projectWorld(cam, headPt); ok {
			splatElectricSegment2D(c, hx, hy, hz, hx+0.01, hy+0.01, hz, 0.75, 1.48, coreCol, sheathCol, amp*1.2*st.reveal, 1.2)
		}
		for k := range 4 {
			sKey := seed ^ uint32(k*4093+st.frame*29)
			spAng := headAng - dir*(0.10+0.75*hashUnit(sKey))
			spRad := g.majorR + (0.55+0.95*hashUnit(sKey+7))*hashSigned(sKey+13)
			spPt := vec3{
				x: g.center.x + spRad*math.Cos(spAng),
				y: g.center.y + spRad*math.Sin(spAng),
				z: g.center.z - g.minorR*0.8,
			}
			if sx, sy, sz, ok := projectWorld(cam, spPt); ok {
				splatAdditiveSubpixel(c, int(math.Round(sx)), int(math.Round(sy)), sz, coreCol, amp*0.92*st.reveal, 1.0)
			}
		}
	}

	drawTorusBolt(o1, infPhase, 1.0, 0.08, 4111)
	drawTorusBolt(o2, math.Pi-infPhase, -1.0, 0.88, 5237)

	// Live crossover Tesla spark jumping across the narrow bridge between 'o' and 'o'
	// whenever the two counter-orbiting plasma heads converge near the center pinch!
	pinch := math.Pow(math.Max(0, math.Cos(infPhase)), 4.0)
	if pinch > 0.10 {
		bridgeA := vec3{o1.center.x + o1.majorR, o1.center.y, o1.center.z - 0.35}
		bridgeB := vec3{o2.center.x - o2.majorR, o2.center.y, o2.center.z - 0.35}
		bridgeC := bridgeA.mix(bridgeB, 0.5).add(vec3{0, 0.42 * math.Sin(st.timeSec*16.0), -0.48})
		drawElectricBolt3D(st, c, bridgeA, bridgeC, bridgeB, 0.5, pinch*amp*1.18, 0.50, false, 6311, true)
	}
}

// renderMorphVortex unleashes an airy, sparkling 3D electromagnetic curl-vortex
// right inside the active phase-transition wavefront while Shape A remains solid
// ahead of the wave and Shape B crystallizes solid behind it:
//   - Crisp pinpoint 3D starlight sparks peeling along helical magnetic streamlines
//   - Slender hero plasma comet trails curving across the transition gap
//   - Live crackling 3D Tesla tether bolts bridging dissolving and forming anchors
func renderMorphVortex(st *sceneState, c *frameCanvas) {
	if st.morph <= 0 || st.fromShape == st.toShape {
		return
	}
	cam := &st.cam
	pal := &st.pal
	prevMorph := math.Max(0, st.morph-0.032)

	evalParticlePos := func(tr morphTriple, m float64) (vec3, float64, float64) {
		wave := tr.wave
		if st.morphDir < 0 {
			wave = 1.0 - wave
		}
		s := morphLocalT(m, wave)
		a := tr.pointFor(st.fromShape)
		b := tr.pointFor(st.toShape)
		p := a.mix(b, s)

		arc := math.Sin(math.Pi * s)
		if arc > 0.001 {
			// Helical magnetic field curl with crisp spatial dispersion.
			twist := arc * 1.45 * st.morphDir
			ct, stw := math.Cos(twist), math.Sin(twist)
			py := p.y*ct - p.z*stw
			pz := p.y*stw + p.z*ct
			p.y = py
			p.z = pz + arc*1.80*math.Sin((wave-0.5)*2.0*math.Pi)*st.morphDir
			p.x += hashSigned(tr.seed+3) * 0.55 * arc
			p.y += hashSigned(tr.seed+9) * 0.65 * arc
		}
		return p, s, arc
	}

	// 1. Airy 3D starlight constellation of pinpoint sparks + slender hero comets
	//    (strided cleanly so negative space sparkles instead of clumping into a blob!).
	for i := 0; i < len(morphTriples); i += 2 {
		tr := morphTriples[i]
		pCur, s, arc := evalParticlePos(tr, st.morph)
		if s <= 0.06 || s >= 0.94 {
			continue
		}

		harm := clamp01(0.15 + 0.85*arc)
		coreCol, sheathCol := electricColors(pal, harm)
		intensity := (0.42 + 0.88*arc) * st.reveal

		x1, y1, z1, ok1 := projectWorld(cam, pCur)
		if !ok1 {
			continue
		}

		// Every 32nd particle becomes a slender velocity-stretched hero comet ribbon;
		// all others render as ultra-crisp pinpoint 1-dot 3D sparks!
		if (i%64) == 0 && arc > 0.25 {
			pPrev, _, _ := evalParticlePos(tr, prevMorph)
			if x0, y0, z0, ok0 := projectWorld(cam, pPrev); ok0 {
				splatElectricSegment2D(c, x0, y0, z0, x1, y1, z1, 0.26, 0.52, coreCol, sheathCol, intensity*0.95, 0.65)
			}
		} else {
			splatAdditiveSubpixel(c, int(math.Round(x1)), int(math.Round(y1)), z1, sheathCol.mix(coreCol, arc), intensity*0.95, 0.65)
		}
	}

	// 2. Live crackling 3D Tesla tether bolts leaping directly from dissolving
	//    anchors of Shape A into crystallizing anchors of Shape B across the gap!
	const numTethers = 5
	for k := range numTethers {
		idx := ((k + 1) * (len(morphTriples) - 1)) / (numTethers + 1)
		tr := morphTriples[idx]
		wave := tr.wave
		if st.morphDir < 0 {
			wave = 1.0 - wave
		}
		s := morphLocalT(st.morph, wave)
		arc := math.Sin(math.Pi * s)
		if arc < 0.22 {
			continue
		}
		a := tr.pointFor(st.fromShape)
		b := tr.pointFor(st.toShape)
		mid := a.mix(b, 0.5).add(vec3{
			x: 0,
			y: 0.85 * math.Sin(float64(k)*2.1+st.timeSec*8.0) * arc,
			z: -1.45 * arc,
		})
		drawElectricBolt3D(st, c, a, mid, b, s, arc*0.98, 0.25+0.70*arc, false, uint32(k*9199+st.fromShape*131+7), true)
	}
}
