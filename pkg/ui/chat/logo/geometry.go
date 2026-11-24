package logo

import (
	"math"
	"sort"
)

func projectWorld(cam *cameraState, p vec3) (sx, sy, cz float64, ok bool) {
	rel := p.sub(cam.pos)
	cz = rel.dot(cam.forward)
	if cz <= 1.0 {
		return 0, 0, cz, false
	}
	cx := rel.dot(cam.right)
	cy := rel.dot(cam.up)
	invZ := cam.focalPx / cz
	return cam.halfW + cx*invZ, cam.halfH + cy*invZ, cz, true
}

func projectSphereBBox(cam *cameraState, center vec3, r float64, w, h int) (x0, x1, y0, y1 int, ok bool) {
	sx, sy, cz, vis := projectWorld(cam, center)
	if !vis || cz <= r+0.5 {
		return 0, 0, 0, 0, false
	}
	sr := r*(cam.focalPx/(cz-r)) + 1.5
	x0 = int(math.Floor(sx - sr))
	x1 = int(math.Ceil(sx + sr))
	y0 = int(math.Floor(sy - sr))
	y1 = int(math.Ceil(sy + sr))
	if x1 < 0 || x0 >= w || y1 < 0 || y0 >= h {
		return 0, 0, 0, 0, false
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= w {
		x1 = w - 1
	}
	if y0 < 0 {
		y0 = 0
	}
	if y1 >= h {
		y1 = h - 1
	}
	return x0, x1, y0, y1, true
}

func projectCapsuleBBox(cam *cameraState, a, b vec3, r float64, w, h int) (x0, x1, y0, y1 int, ok bool) {
	ax0, ax1, ay0, ay1, oka := projectSphereBBox(cam, a, r, w, h)
	bx0, bx1, by0, by1, okb := projectSphereBBox(cam, b, r, w, h)
	if !oka && !okb {
		return 0, 0, 0, 0, false
	}
	if !oka {
		return bx0, bx1, by0, by1, true
	}
	if !okb {
		return ax0, ax1, ay0, ay1, true
	}
	return min(ax0, bx0), max(ax1, bx1), min(ay0, by0), max(ay1, by1), true
}

func waveCoord(worldX, morphDir float64) float64 {
	w := clamp01((worldX + 10.8) / 21.6)
	if morphDir < 0 {
		return 1.0 - w
	}
	return w
}

const morphStagger = 0.56

func morphLocalT(morph, wave float64) float64 {
	return smootherstep((morph - morphStagger*wave) / (1.0 - morphStagger))
}

// morphClipSurface determines whether an analytical surface point is visible
// during a phase-transition morph and computes its molten ionization rim glow.
func morphClipSurface(st *sceneState, worldX float64, isFrom bool) (bool, float64) {
	if st.morph <= 0 || st.fromShape == st.toShape {
		return true, 0
	}
	lt := morphLocalT(st.morph, waveCoord(worldX, st.morphDir))
	if isFrom {
		if lt >= 0.20 {
			return false, 0
		}
		if lt > 0.02 {
			return true, clamp01((lt - 0.02) / 0.18)
		}
		return true, 0
	}
	if lt <= 0.80 {
		return false, 0
	}
	if lt < 0.98 {
		return true, clamp01((0.98 - lt) / 0.18)
	}
	return true, 0
}

func subpixelRay(cam *cameraState, sx, sy int) vec3 {
	px := (float64(sx) - cam.halfW) * cam.invFocalPx
	py := (float64(sy) - cam.halfH) * cam.invFocalPx
	return vec3{
		x: cam.forward.x + cam.right.x*px + cam.up.x*py,
		y: cam.forward.y + cam.right.y*px + cam.up.y*py,
		z: cam.forward.z + cam.right.z*px + cam.up.z*py,
	}.normalize()
}

func intersectSphere(ro, rd, center vec3, r float64) (float64, bool) {
	oc := ro.sub(center)
	b := oc.dot(rd)
	c := oc.dot(oc) - r*r
	disc := b*b - c
	if disc < 0 {
		return 0, false
	}
	t := -b - math.Sqrt(disc)
	if t <= 0.5 {
		return 0, false
	}
	return t, true
}

func intersectCapsule(ro, rd, pa, pb vec3, r float64) (t, u float64, n vec3, hit bool) {
	ba := pb.sub(pa)
	oa := ro.sub(pa)
	baba := ba.dot(ba)
	if baba < 1e-9 {
		if ts, ok := intersectSphere(ro, rd, pa, r); ok {
			p := ro.add(rd.mul(ts))
			return ts, 0.5, p.sub(pa).normalize(), true
		}
		return 0, 0, vec3{}, false
	}
	bard := ba.dot(rd)
	baoa := ba.dot(oa)
	rdoa := rd.dot(oa)
	oaoa := oa.dot(oa)

	a := baba - bard*bard
	b := baba*rdoa - baoa*bard
	c := baba*oaoa - baoa*baoa - r*r*baba
	h := b*b - a*c
	if h >= 0 {
		tc := (-b - math.Sqrt(h)) / a
		y := baoa + tc*bard
		if y > 0 && y < baba {
			if tc > 0.5 {
				u = y / baba
				p := ro.add(rd.mul(tc))
				axisPt := pa.add(ba.mul(u))
				return tc, u, p.sub(axisPt).mul(1.0 / r), true
			}
			return 0, 0, vec3{}, false
		}
		// Intersect nearest spherical endcap.
		var oc vec3
		var capU float64
		if y <= 0 {
			oc = oa
			capU = 0
		} else {
			oc = ro.sub(pb)
			capU = 1
		}
		cb := rd.dot(oc)
		cc := oc.dot(oc) - r*r
		ch := cb*cb - cc
		if ch >= 0 {
			ts := -cb - math.Sqrt(ch)
			if ts > 0.5 {
				p := ro.add(rd.mul(ts))
				endPt := pa
				if capU > 0.5 {
					endPt = pb
				}
				return ts, capU, p.sub(endPt).mul(1.0 / r), true
			}
		}
	}
	return 0, 0, vec3{}, false
}

func intersectTorusXY(ro, rd, center vec3, majorR, minorR float64) (t, theta float64, n vec3, hit bool) {
	// Bounding sphere test for fast early rejection.
	boundR := majorR + minorR + 0.05
	oc := ro.sub(center)
	b := oc.dot(rd)
	c := oc.dot(oc) - boundR*boundR
	disc := b*b - c
	if disc < 0 {
		return 0, 0, vec3{}, false
	}
	sq := math.Sqrt(disc)
	tNear := -b - sq
	tFar := -b + sq
	if tFar <= 0.5 {
		return 0, 0, vec3{}, false
	}
	if tNear < 0.5 {
		tNear = 0.5
	}

	curT := tNear
	for range 14 {
		if curT > tFar {
			return 0, 0, vec3{}, false
		}
		px := oc.x + rd.x*curT
		py := oc.y + rd.y*curT
		pz := oc.z + rd.z*curT
		rho := math.Hypot(px, py)
		q := rho - majorR
		d := math.Hypot(q, pz) - minorR
		if d < 0.014 {
			var rx, ry float64
			if rho > 1e-6 {
				invRho := majorR / rho
				rx = px * invRho
				ry = py * invRho
			}
			norm := vec3{px - rx, py - ry, pz}.normalize()
			return curT, math.Atan2(py, px), norm, true
		}
		curT += d
	}
	return 0, 0, vec3{}, false
}

// refractCoreChord computes Snell's Law refraction (IOR 1.42) of a view ray
// entering a neural soma cell, returning optical chord length through the
// glowing inner plasma nucleus.
func refractCoreChord(p, rd, n vec3, center vec3, radius float64) float64 {
	const eta = 1.0 / 1.42
	cosI := clamp01(-rd.dot(n))
	sinT2 := eta * eta * (1.0 - cosI*cosI)
	if sinT2 >= 1.0 {
		return 0
	}
	cosT := math.Sqrt(1.0 - sinT2)
	tr := rd.mul(eta).add(n.mul(eta*cosI - cosT))
	coreR := radius * 0.54
	oc := p.sub(center)
	b := oc.dot(tr)
	c := oc.dot(oc) - coreR*coreR
	disc := b*b - c
	if disc <= 0 {
		return 0
	}
	return clamp01(math.Sqrt(disc) / coreR)
}

// shadeGlassSurface computes 3D studio shading + internal electric glow for
// translucent crystalline neural orbs, code prism bars, and wordmark tubes.
func shadeGlassSurface(st *sceneState, rd, n vec3, cz, energy, coreGlow, veinGlow, harmonic, rimHot float64) (rgbLinear, float64) {
	pal := &st.pal
	v := rd.mul(-1.0)
	ndotv := clamp01(n.dot(v))

	ndotlKey := n.dot(st.keyLight)
	diff := clamp01((ndotlKey + 0.18) / 1.18)
	fill := clamp01((n.dot(st.fillLight) + 0.16) / 1.16)

	// Crisp GGX-like studio microfacet specular glint.
	hVec := st.keyLight.add(v).normalize()
	ndoth := clamp01(n.dot(hVec))
	spec := math.Pow(ndoth, 18.0) * clamp01(ndotlKey+0.15)

	// Thin-film Schlick Fresnel rim separating 3D volume clearly against dark/light canvas.
	fresnel := math.Pow(1.0-ndotv, 2.5)
	rimBack := clamp01(n.dot(st.rimLight)*0.50 + 0.50)

	// Depth cue gently separates near hidden core from deeper input/output layers.
	depthCue := clamp01(1.0 - 0.055*(cz-st.cam.focalDist))
	depthCue = 0.72 + 0.32*depthCue

	// Active electric hue blends smoothly between Primary (forward) and Highlight (backprop/core).
	elecHue := pal.primary.mix(pal.highlight, clamp01(harmonic))
	hotCol := elecHue.mix(pal.coreHot, clamp01(0.20+0.42*coreGlow+0.35*rimHot))

	totalE := clamp01(math.Max(energy, math.Max(veinGlow, rimHot)))

	if pal.light {
		// Light canvas: dormant 3D volume shades in calm light-slate; electric
		// discharge drops dramatically into deep vivid saturated theme ink!
		base := pal.shellMid.mix(pal.shellDark, 0.22+0.55*(1.0-diff))
		// Keep dormant structure softly recessed toward paper so electricity pops cleanly.
		dormant := pal.background.mix(base, 0.56)
		activeInk := elecHue.mul(0.82).mix(pal.coreHot, clamp01(0.35*coreGlow+0.58*veinGlow+0.48*rimHot))
		lit := dormant.mix(activeInk, clamp01(totalE*1.12))
		glint := pal.background.mix(lit, 0.65)
		out := lit.mix(glint, 0.28*spec*(1.0-totalE*0.6))
		return out, totalE
	}

	// Dark canvas: moody sculpted dark-glass body with crisp Fresnel rim so
	// incandescent internal nuclei, crackling veins, and external arcs EXPLODE visually.
	shell := pal.shellDark.mix(pal.shellMid, 0.18+0.62*diff+0.14*fill)
	rimCol := pal.secondary.mix(elecHue, 0.35+0.65*totalE)

	ambient := shell.mul(0.24)
	diffuse := shell.mix(elecHue, 0.68*energy).mul(0.56*diff + 0.14*fill)
	rim := rimCol.mul((0.50*fresnel*rimBack + 0.18*fresnel) * (0.58 + 1.05*totalE))
	specular := elecHue.mix(pal.coreHot, 0.58).mul(0.68 * spec * (0.48 + 0.72*totalE))
	coreRad := hotCol.mul(1.55 * coreGlow * (0.28 + 0.72*energy))
	veinRad := hotCol.mul(1.85 * veinGlow)
	moltenRad := pal.highlight.mix(pal.coreHot, 0.45).mul(1.95 * rimHot)

	out := ambient.add(diffuse).add(rim).add(specular).add(coreRad).add(veinRad).add(moltenRad).mul(depthCue)
	return out, totalE
}

func rasterizeNeuralNodes(st *sceneState, c *frameCanvas, isFrom bool) {
	cam := &st.cam
	w, h := c.width, c.height

	for i := range st.nodes {
		n := &st.nodes[i]
		x0, x1, y0, y1, ok := projectSphereBBox(cam, n.center, n.radius, w, h)
		if !ok {
			continue
		}
		// Precompute rotating membrane arc axes for crackling surface veins when excited.
		ph := n.phase
		ax1 := vec3{math.Cos(ph), math.Sin(ph) * 0.7, 0.5}.normalize()
		ax2 := vec3{-math.Sin(ph * 1.3), math.Cos(ph * 1.3), -0.4}.normalize()

		for sy := y0; sy <= y1; sy++ {
			rowOff := sy * w
			for sx := x0; sx <= x1; sx++ {
				rd := subpixelRay(cam, sx, sy)
				t, hit := intersectSphere(cam.pos, rd, n.center, n.radius)
				if !hit {
					continue
				}
				p := cam.pos.add(rd.mul(t))
				vis, rimHot := morphClipSurface(st, p.x, isFrom)
				if !vis {
					continue
				}
				cz := p.sub(cam.pos).dot(cam.forward)
				idx := rowOff + sx
				if cz >= c.depth[idx] {
					continue
				}

				norm := p.sub(n.center).mul(1.0 / n.radius)

				// Refracted inner plasma nucleus visible inside the 3D glass orb.
				chord := refractCoreChord(p, rd, norm, n.center, n.radius)
				coreGlow := math.Pow(chord, 1.25) * (0.12 + 0.92*n.energy)

				// Live crackling membrane lightning veins racing across fired orbs!
				veinGlow := 0.0
				if n.energy > 0.16 {
					w1 := norm.dot(ax1) + 0.16*math.Sin(norm.y*7.5+ph*2.6)
					w2 := norm.dot(ax2) + 0.16*math.Cos(norm.x*7.5-ph*2.2)
					v1 := math.Exp(-(w1 * w1) / 0.011)
					v2 := math.Exp(-(w2 * w2) / 0.011)
					veinGlow = math.Max(v1, v2) * clamp01((n.energy-0.16)/0.65)
				}

				col, en := shadeGlassSurface(st, rd, norm, cz, n.energy, coreGlow, veinGlow, n.harmonic, rimHot)
				c.depth[idx] = cz
				c.solidCov[idx] = 1.0
				c.surfR[idx] = col.r * st.reveal
				c.surfG[idx] = col.g * st.reveal
				c.surfB[idx] = col.b * st.reveal
				c.surfEnergy[idx] = en
			}
		}
	}
}

// rasterizeNeuralConduits draws the subtle 3D arched architectural axon rails
// connecting all 16 synapses so the neural topology stays crystal-clear even
// between high-voltage discharges.
func rasterizeNeuralConduits(st *sceneState, c *frameCanvas, isFrom bool) {
	cam := &st.cam
	pal := &st.pal
	railCol := pal.shellDark.mix(pal.shellMid, 0.55)
	if pal.light {
		railCol = pal.shellMid.mix(pal.background, 0.28)
	} else {
		railCol = railCol.mul(0.55)
	}

	const segs = 12
	for i := range st.edges {
		e := &st.edges[i]
		prev := e.a
		px, py, pz, prevOK := projectWorld(cam, prev)
		for s := 1; s <= segs; s++ {
			t := float64(s) / float64(segs)
			cur := evalQuadBezier(e.a, e.ctrl, e.b, t)
			cx, cy, cz, curOK := projectWorld(cam, cur)
			if prevOK && curOK {
				midX := (prev.x + cur.x) * 0.5
				if vis, rimHot := morphClipSurface(st, midX, isFrom); vis {
					col := railCol
					en := 0.04
					if rimHot > 0 {
						col = col.mix(pal.highlight.mix(pal.coreHot, 0.45), rimHot)
						en = rimHot
					}
					splatSolidSegment2D(c, px, py, pz, cx, cy, cz, 0.62, col.mul(st.reveal), en)
				}
			}
			prev, px, py, pz, prevOK = cur, cx, cy, cz, curOK
		}
	}
}

func splatSolidSegment2D(c *frameCanvas, x0, y0, z0, x1, y1, z1, radius float64, col rgbLinear, energy float64) {
	dx, dy := x1-x0, y1-y0
	segLenSq := dx*dx + dy*dy
	minX := int(math.Floor(math.Min(x0, x1) - radius - 0.5))
	maxX := int(math.Ceil(math.Max(x0, x1) + radius + 0.5))
	minY := int(math.Floor(math.Min(y0, y1) - radius - 0.5))
	maxY := int(math.Ceil(math.Max(y0, y1) + radius + 0.5))
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

	rSq := radius * radius
	for sy := minY; sy <= maxY; sy++ {
		rowOff := sy * c.width
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
			if dSq > rSq {
				continue
			}
			cz := z0 + (z1-z0)*h
			idx := rowOff + sx
			if cz >= c.depth[idx] {
				continue
			}
			cov := clamp01(1.0 - math.Sqrt(dSq)/math.Max(0.25, radius)*0.55)
			c.depth[idx] = cz
			c.solidCov[idx] = cov
			c.surfR[idx] = col.r
			c.surfG[idx] = col.g
			c.surfB[idx] = col.b
			c.surfEnergy[idx] = energy
		}
	}
}

func rasterizeCodePrism(st *sceneState, c *frameCanvas, isFrom bool) {
	cam := &st.cam
	w, h := c.width, c.height

	for i := range st.bars {
		bar := &st.bars[i]
		x0, x1, y0, y1, ok := projectCapsuleBBox(cam, bar.a, bar.b, bar.radius, w, h)
		if !ok {
			continue
		}
		for sy := y0; sy <= y1; sy++ {
			rowOff := sy * w
			for sx := x0; sx <= x1; sx++ {
				rd := subpixelRay(cam, sx, sy)
				t, u, norm, hit := intersectCapsule(cam.pos, rd, bar.a, bar.b, bar.radius)
				if !hit {
					continue
				}
				p := cam.pos.add(rd.mul(t))
				vis, rimHot := morphClipSurface(st, p.x, isFrom)
				if !vis {
					continue
				}
				cz := p.sub(cam.pos).dot(cam.forward)
				idx := rowOff + sx
				if cz >= c.depth[idx] {
					continue
				}

				// Sharp traveling electric charge wave racing down the prism bar.
				pulse := 0.0
				if bar.chargeAmp > 0.01 {
					behind := bar.chargeT - u
					if behind >= 0 {
						pulse = math.Exp(-(behind*behind)/0.055) * bar.chargeAmp
					} else {
						pulse = math.Exp(-(behind*behind)/0.008) * bar.chargeAmp
					}
				}
				energy := clamp01(bar.baseEnergy + 0.92*pulse)
				coreGlow := math.Pow(clamp01(norm.dot(rd.mul(-1.0))), 1.35) * energy
				veinGlow := pulse * pulse * 0.85

				col, en := shadeGlassSurface(st, rd, norm, cz, energy, coreGlow, veinGlow, bar.harmonic, rimHot)
				c.depth[idx] = cz
				c.solidCov[idx] = 1.0
				c.surfR[idx] = col.r * st.reveal
				c.surfG[idx] = col.g * st.reveal
				c.surfB[idx] = col.b * st.reveal
				c.surfEnergy[idx] = en
			}
		}
	}
}

func rasterizeWordmark(st *sceneState, c *frameCanvas, isFrom bool) {
	cam := &st.cam
	w, h := c.width, c.height

	// Active figure-eight angle on the two 'o' toruses so the torus surface
	// glows dynamically right underneath the orbiting plasma current.
	infPhase := st.loopCircuitPhase * 2.0 * math.Pi

	for i := range st.word {
		g := &st.word[i]
		// Capsule stem for 'l' (kind 0) and 'p' (kind 2).
		if g.kind == 0 || g.kind == 2 {
			x0, x1, y0, y1, ok := projectCapsuleBBox(cam, g.a, g.b, g.minorR, w, h)
			if ok {
				for sy := y0; sy <= y1; sy++ {
					rowOff := sy * w
					for sx := x0; sx <= x1; sx++ {
						rd := subpixelRay(cam, sx, sy)
						t, u, norm, hit := intersectCapsule(cam.pos, rd, g.a, g.b, g.minorR)
						if !hit {
							continue
						}
						p := cam.pos.add(rd.mul(t))
						vis, rimHot := morphClipSurface(st, p.x, isFrom)
						if !vis {
							continue
						}
						cz := p.sub(cam.pos).dot(cam.forward)
						idx := rowOff + sx
						if cz >= c.depth[idx] {
							continue
						}
						pulse := 0.0
						if g.pulseAmp > 0.01 {
							behind := g.pulseU - u
							if behind >= 0 {
								pulse = math.Exp(-(behind*behind)/0.07) * g.pulseAmp
							} else {
								pulse = math.Exp(-(behind*behind)/0.012) * g.pulseAmp
							}
						}
						en := clamp01(g.energy*0.65 + 0.90*pulse)
						coreGlow := math.Pow(clamp01(norm.dot(rd.mul(-1.0))), 1.35) * en
						col, finalE := shadeGlassSurface(st, rd, norm, cz, en, coreGlow, pulse*pulse*0.75, g.harmonic, rimHot)
						c.depth[idx] = cz
						c.solidCov[idx] = 1.0
						c.surfR[idx] = col.r * st.reveal
						c.surfG[idx] = col.g * st.reveal
						c.surfB[idx] = col.b * st.reveal
						c.surfEnergy[idx] = finalE
					}
				}
			}
		}

		// 3D Torus ring for 'o', 'o' (kind 1) and 'p' bowl (kind 2).
		if g.kind == 1 || g.kind == 2 {
			x0, x1, y0, y1, ok := projectSphereBBox(cam, g.center, g.majorR+g.minorR, w, h)
			if !ok {
				continue
			}
			for sy := y0; sy <= y1; sy++ {
				rowOff := sy * w
				for sx := x0; sx <= x1; sx++ {
					rd := subpixelRay(cam, sx, sy)
					t, theta, norm, hit := intersectTorusXY(cam.pos, rd, g.center, g.majorR, g.minorR)
					if !hit {
						continue
					}
					p := cam.pos.add(rd.mul(t))
					vis, rimHot := morphClipSurface(st, p.x, isFrom)
					if !vis {
						continue
					}
					cz := p.sub(cam.pos).dot(cam.forward)
					idx := rowOff + sx
					if cz >= c.depth[idx] {
						continue
					}

					// Angular plasma excitement matching the orbiting figure-eight current.
					angPulse := 0.0
					if i == 1 {
						// Counter-clockwise orbit around first 'o'.
						dAng := math.Atan2(math.Sin(theta-infPhase), math.Cos(theta-infPhase))
						angPulse = math.Exp(-(dAng*dAng)/0.45) * g.pulseAmp
					} else if i == 2 {
						// Clockwise mirrored figure-eight orbit around second 'o'.
						targetAng := math.Pi - infPhase
						dAng := math.Atan2(math.Sin(theta-targetAng), math.Cos(theta-targetAng))
						angPulse = math.Exp(-(dAng*dAng)/0.45) * g.pulseAmp
					} else {
						// 'p' bowl glows as the pulse sweeps out through 'p'.
						angPulse = g.energy * 0.85
					}

					en := clamp01(g.energy*0.55 + 0.88*angPulse)
					coreGlow := math.Pow(clamp01(norm.dot(rd.mul(-1.0))), 1.35) * en
					col, finalE := shadeGlassSurface(st, rd, norm, cz, en, coreGlow, angPulse*angPulse*0.75, g.harmonic, rimHot)
					c.depth[idx] = cz
					c.solidCov[idx] = 1.0
					c.surfR[idx] = col.r * st.reveal
					c.surfG[idx] = col.g * st.reveal
					c.surfB[idx] = col.b * st.reveal
					c.surfEnergy[idx] = finalE
				}
			}
		}
	}
}

// morphTriple stores coherent 1-to-1 mapped 3D surface anchors across all three
// shapes for seamless electromagnetic curl-vortex morphogenesis.
type morphTriple struct {
	net  vec3
	code vec3
	word vec3
	wave float64
	seed uint32
}

var morphTriples = buildMorphTriples()

func buildMorphTriples() []morphTriple {
	// 1. Sample 3D Neural Network (10 Fibonacci spheres + 16 arched Bezier axons).
	var rawNet []vec3
	ga := math.Pi * (3.0 - math.Sqrt(5.0))
	for _, n := range neuralNodes {
		const nodePts = 220
		for i := range nodePts {
			y := 1.0 - (2.0*float64(i)+1.0)/float64(nodePts)
			rad := math.Sqrt(math.Max(0, 1.0-y*y))
			th := ga * float64(i)
			rawNet = append(rawNet, vec3{
				x: n.base.x + n.radius*math.Cos(th)*rad,
				y: n.base.y + n.radius*y,
				z: n.base.z + n.radius*math.Sin(th)*rad,
			})
		}
	}
	for _, e := range neuralEdges {
		a := neuralNodes[e.from].base
		b := neuralNodes[e.to].base
		mid := a.add(b).mul(0.5)
		mid.y += e.archY
		mid.z += e.archZ
		const edgePts = 52
		for s := range edgePts {
			t := float64(s) / float64(edgePts-1)
			rawNet = append(rawNet, evalQuadBezier(a, mid, b, t))
		}
	}

	// 2. Sample 3D "</>" Code Prism capsules.
	var rawCode []vec3
	codeDefs := [numCodeBars]struct {
		a, b vec3
		r    float64
	}{
		{vec3{-9.2, 0.0, 1.15}, vec3{-4.3, -3.55, 0.25}, 0.60},
		{vec3{-9.2, 0.0, 1.15}, vec3{-4.3, 3.55, 0.25}, 0.60},
		{vec3{1.90, -4.60, -1.45}, vec3{-1.90, 4.60, -1.45}, 0.66},
		{vec3{4.3, -3.55, 0.25}, vec3{9.2, 0.0, 1.15}, 0.60},
		{vec3{4.3, 3.55, 0.25}, vec3{9.2, 0.0, 1.15}, 0.60},
	}
	sampleCapsule := func(dst *[]vec3, a, b vec3, r float64, axial, rings int) {
		dir := b.sub(a).normalize()
		ref := vec3{0, 0, 1}
		if math.Abs(dir.dot(ref)) > 0.9 {
			ref = vec3{0, 1, 0}
		}
		uAxis := dir.cross(ref).normalize()
		vAxis := dir.cross(uAxis).normalize()
		for i := range axial {
			t := float64(i) / float64(axial-1)
			c := a.mix(b, t)
			for j := range rings {
				ang := 2.0 * math.Pi * float64(j) / float64(rings)
				off := uAxis.mul(math.Cos(ang) * r).add(vAxis.mul(math.Sin(ang) * r))
				*dst = append(*dst, c.add(off))
			}
		}
	}
	for idx, d := range codeDefs {
		axial := 52
		if idx == 2 {
			axial = 72
		}
		sampleCapsule(&rawCode, d.a, d.b, d.r, axial, 10)
	}

	// 3. Sample 3D "loop" Wordmark (capsules + true 3D toruses).
	var rawWord []vec3
	const shiftX = -0.35
	const shiftY = -0.10
	const tubeR = 0.56
	sampleCapsule(&rawWord, vec3{-8.50 + shiftX, -4.20 + shiftY, 0.45}, vec3{-8.50 + shiftX, 2.50 + shiftY, 0.45}, tubeR, 56, 10)
	sampleTorus := func(dst *[]vec3, center vec3, majorR, minorR float64, majSteps, minSteps int) {
		for i := range majSteps {
			th := 2.0 * math.Pi * float64(i) / float64(majSteps)
			ct, st := math.Cos(th), math.Sin(th)
			for j := range minSteps {
				ph := 2.0 * math.Pi * float64(j) / float64(minSteps)
				cp, sp := math.Cos(ph), math.Sin(ph)
				rad := majorR + minorR*cp
				*dst = append(*dst, vec3{
					x: center.x + rad*ct,
					y: center.y + rad*st,
					z: center.z + minorR*sp,
				})
			}
		}
	}
	sampleTorus(&rawWord, vec3{-4.45 + shiftX, 0.40 + shiftY, -0.58}, 2.05, tubeR, 84, 10)
	sampleTorus(&rawWord, vec3{1.25 + shiftX, 0.40 + shiftY, -0.58}, 2.05, tubeR, 84, 10)
	sampleCapsule(&rawWord, vec3{5.35 + shiftX, -1.85 + shiftY, 0.42}, vec3{5.35 + shiftX, 4.55 + shiftY, 0.42}, tubeR*0.96, 52, 10)
	sampleTorus(&rawWord, vec3{7.30 + shiftX, 0.40 + shiftY, 0.42}, 1.95, tubeR*0.96, 76, 10)

	// Sort each point cloud coherently left-to-right (primary X, secondary Y)
	// so particles travel along graceful non-tangled magnetic field lines.
	sortCloud := func(pts []vec3) {
		sort.Slice(pts, func(i, j int) bool {
			bx := int(math.Floor(pts[i].x * 1.4))
			bj := int(math.Floor(pts[j].x * 1.4))
			if bx != bj {
				return bx < bj
			}
			return pts[i].y < pts[j].y
		})
	}
	sortCloud(rawNet)
	sortCloud(rawCode)
	sortCloud(rawWord)

	const targetCount = 2600
	triples := make([]morphTriple, targetCount)
	for i := range targetCount {
		u := float64(i) / float64(targetCount-1)
		nPt := rawNet[int(u*float64(len(rawNet)-1))]
		cPt := rawCode[int(u*float64(len(rawCode)-1))]
		wPt := rawWord[int(u*float64(len(rawWord)-1))]
		avgX := (nPt.x + cPt.x + wPt.x) / 3.0
		triples[i] = morphTriple{
			net:  nPt,
			code: cPt,
			word: wPt,
			wave: clamp01((avgX + 10.8) / 21.6),
			seed: hashU32(uint32(i)*2654435761 + 17),
		}
	}
	return triples
}

func (m morphTriple) pointFor(shape int) vec3 {
	switch shape {
	case shapeNeural:
		return m.net
	case shapeCode:
		return m.code
	default:
		return m.word
	}
}
