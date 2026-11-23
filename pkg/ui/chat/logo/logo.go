package logo

import (
	"image/color"
	"math"
	"sort"
	"strings"

	"loop/pkg/ui/style"
)

const (
	EntranceFrames = 18
	CycleFrames    = 337
	ReducedFrame   = 270
)

// Appearance configures contrast and theme accents for the 3D Braille startup logo.
type Appearance struct {
	Brightness int
	Light      bool
	Primary    color.Color
	Highlight  color.Color
}

func smootherstep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * t * (t*(t*6-15) + 10)
}

func colorToRGB(c color.Color, fr, fg, fb float64) (float64, float64, float64) {
	if c == nil {
		return fr, fg, fb
	}
	r, g, b, _ := c.RGBA()
	return float64(r >> 8), float64(g >> 8), float64(b >> 8)
}

// Render draws a single frame of the 3-stage 3D Braille startup animation
// (neural network, "</>" code sign, and "loop" wordmark) sized to cols x rows.
func Render(cols, rows, frame int, appearance ...Appearance) []string {
	width, height := cols*2, rows*4
	light := make([]float64, width*height)
	accent := make([]float64, width*height)
	depth := make([]float64, len(light))
	for i := range depth {
		depth[i] = math.Inf(1)
	}
	scale := math.Min(float64(width-6)/27.0, float64(height-6)/14.5)
	scale = math.Max(0.1, scale)

	// Continuous 3-stage timeline (at 60ms/frame, ~19s cycle):
	//   [0..17]    startup ease-in of the 3D 3-4-3 neural network
	//   [18..112]  3D neural network (~5.7s): two crisp synchronized forward-pass waves
	//              (Left -> Center -> Right) with theme-tinted glowing packets & firing orbs
	//   [113..139] 3D morph + twist: neural network -> "</>" code sign (~1.6s)
	//   [140..209] 3D "</>" code sign (~4.2s) with 3D sway + theme-tinted light sweep
	//   [210..234] 3D morph + twist: "</>" code sign -> "loop" wordmark (~1.5s)
	//   [235..309] 3D "loop" wordmark (~4.5s) with gentle 3D sway + light sweep
	//   [310..336] 3D morph + twist: "loop" wordmark -> neural network (~1.6s)
	const (
		netEnd        = 113
		morphNetCode  = 140
		codeEnd       = 210
		morphCodeWord = 235
		wordEnd       = 310
	)

	const (
		shapeNet  = 0
		shapeCode = 1
		shapeWord = 2
	)

	var (
		fromShape  = shapeNet
		toShape    = shapeNet
		morph      float64
		morphDir   = 1.0
		reveal     = 1.0
		zoom       = 1.0
		yaw        float64
		pitch      float64
		sweepPhase float64
		netWave    float64 // [0..1) phase within a Left -> Center -> Right forward pass
		netActive  float64
	)

	switch {
	case frame < EntranceFrames:
		p := float64(frame) / float64(EntranceFrames)
		e := 1 - math.Pow(1-p, 3)
		fromShape, toShape = shapeNet, shapeNet
		reveal = math.Min(1, p*1.45)
		zoom = 0.78 + 0.22*e
		yaw = -0.36 * (1 - e)
		pitch = 0.16 * (1 - e)
		sweepPhase = p * 0.6
		netWave = p * 0.12
		netActive = e
	case frame < netEnd:
		u := float64(frame-EntranceFrames) / float64(netEnd-EntranceFrames)
		fromShape, toShape = shapeNet, shapeNet
		// Restrained holographic 3D tilt keeps all 3 columns crystal-clear and unobstructed.
		yaw = 0.28 * math.Sin(2*math.Pi*u)
		pitch = 0.11 * math.Sin(2*math.Pi*u)
		sweepPhase = 0.6 + u*2*math.Pi
		// Two full synchronized Left -> Center -> Right forward-pass cascades.
		netWave = math.Mod(u*2.0, 1.0)
		netActive = 1.0
	case frame < morphNetCode:
		u := float64(frame-netEnd) / float64(morphNetCode-netEnd)
		fromShape, toShape = shapeNet, shapeCode
		morph = u
		morphDir = 1
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = 0.36 * arc
		pitch = -0.15 * arc
		sweepPhase = 0.6 + 2*math.Pi + u*math.Pi
		netWave = 0.95
		netActive = 1 - u
	case frame < codeEnd:
		u := float64(frame-morphNetCode) / float64(codeEnd-morphNetCode)
		fromShape, toShape = shapeCode, shapeCode
		yaw = 0.38 * math.Sin(2*math.Pi*u)
		pitch = 0.12 * math.Sin(2*math.Pi*u)
		sweepPhase = 0.6 + 3*math.Pi + u*2*math.Pi
	case frame < morphCodeWord:
		u := float64(frame-codeEnd) / float64(morphCodeWord-codeEnd)
		fromShape, toShape = shapeCode, shapeWord
		morph = u
		morphDir = 1
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = -0.34 * arc
		pitch = 0.14 * arc
		sweepPhase = 0.6 + 5*math.Pi + u*math.Pi
	case frame < wordEnd:
		u := float64(frame-morphCodeWord) / float64(wordEnd-morphCodeWord)
		fromShape, toShape = shapeWord, shapeWord
		yaw = 0.16 * math.Sin(2*math.Pi*u)
		pitch = 0.07 * math.Sin(4*math.Pi*u)
		sweepPhase = 0.6 + 6*math.Pi + u*2*math.Pi
	default:
		u := float64(frame-wordEnd) / float64(CycleFrames-wordEnd)
		fromShape, toShape = shapeWord, shapeNet
		morph = u
		morphDir = -1
		arc := math.Sin(math.Pi * smootherstep(u))
		yaw = 0.36 * arc
		pitch = -0.15 * arc
		sweepPhase = 0.6 + 8*math.Pi + u*math.Pi
		netWave = 0.05
		netActive = u
	}

	// Precompute synchronized forward-pass activation per neuron & packet head per wire.
	// Layer 0 fires around w=0.10, wires 0->1 travel [0.14..0.46],
	// Layer 1 fires around w=0.48, wires 1->2 travel [0.52..0.84],
	// Layer 2 fires around w=0.86.
	var nodeFire [len(neuralNodes)]float64
	var edgeHead [len(neuralEdges)]float64
	var edgeActive [len(neuralEdges)]bool
	if netActive > 0 {
		layerPeak := [3]float64{0.10, 0.48, 0.86}
		for idx, n := range neuralNodes {
			dt := netWave - (layerPeak[n.layer] + n.stagger)
			if dt < -0.5 {
				dt += 1.0
			} else if dt > 0.5 {
				dt -= 1.0
			}
			nodeFire[idx] = math.Exp(-dt * dt / 0.0085)
		}
		for idx, e := range neuralEdges {
			start := 0.13 + e.stagger
			if neuralNodes[e.from].layer == 1 {
				start = 0.51 + e.stagger
			}
			const travelDur = 0.34
			progress := (netWave - start) / travelDur
			if progress >= -0.10 && progress <= 1.15 {
				edgeHead[idx] = progress
				edgeActive[idx] = true
			}
		}
	}

	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)

	plot := func(x, y, z, brightness, tint float64) {
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
				accent[index] = tint
			}
		}
	}

	edgeSpark := func(p logoParticle) float64 {
		idx := p.netIdx
		if !edgeActive[idx] {
			return 0
		}
		behind := edgeHead[idx] - p.netT
		if behind >= 0 {
			return math.Exp(-behind * behind / 0.022)
		}
		return math.Exp(-behind * behind / 0.003)
	}

	shapeParticle := func(triple logoMorphTriple, shape int) logoParticle {
		switch shape {
		case shapeNet:
			p := triple.net
			if netActive > 0 {
				if p.netKind == netKindNode {
					if fire := nodeFire[p.netIdx]; fire > 0.04 {
						n := neuralNodes[p.netIdx]
						expand := 1.0 + 0.14*fire*netActive
						p.x = n.x + (p.x-n.x)*expand
						p.y = n.y + (p.y-n.y)*expand
						p.z = n.z + (p.z-n.z)*expand
					}
				} else if p.netKind == netKindEdge {
					if spk := edgeSpark(p); spk > 0.10 {
						bloom := 0.22 * spk * netActive
						p.x += p.nx * bloom
						p.y += p.ny * bloom
					}
				}
			}
			return p
		case shapeCode:
			return triple.code
		default:
			return triple.word
		}
	}

	neuralShadeAndTint := func(p logoParticle) (float64, float64) {
		if p.netKind == netKindNode {
			fire := nodeFire[p.netIdx]
			return 0.92 + 0.95*fire, math.Min(1.0, fire*1.15)
		}
		spk := edgeSpark(p)
		return 0.36 + 2.10*spk, math.Min(1.0, spk*1.25)
	}

	activeScale := scale * zoom
	for i := range logoMorphTriples {
		triple := logoMorphTriples[i]

		var px, py, pz, nx, ny, nz float64
		shadeBoost := 1.0
		tint := 0.0
		if fromShape == toShape || morph <= 0 {
			p := shapeParticle(triple, fromShape)
			px, py, pz = p.x, p.y, p.z
			nx, ny, nz = p.nx, p.ny, p.nz
			if fromShape == shapeNet {
				shadeBoost, tint = neuralShadeAndTint(p)
			}
		} else {
			a := shapeParticle(triple, fromShape)
			b := shapeParticle(triple, toShape)

			wave := triple.wave
			if morphDir < 0 {
				wave = 1 - wave
			}
			const stagger = 0.22
			localT := (morph - stagger*wave) / (1 - stagger)
			s := smootherstep(localT)

			px = a.x + (b.x-a.x)*s
			py = a.y + (b.y-a.y)*s
			pz = a.z + (b.z-a.z)*s

			nx = a.nx + (b.nx-a.nx)*s
			ny = a.ny + (b.ny-a.ny)*s
			nz = a.nz + (b.nz-a.nz)*s

			arc := math.Sin(math.Pi * s)
			twist := arc * 1.10 * morphDir
			ct, st := math.Cos(twist), math.Sin(twist)
			py, pz = py*ct-pz*st, py*st+pz*ct
			ny, nz = ny*ct-nz*st, ny*st+nz*ct
			pz += arc * 1.35 * math.Sin((wave-0.5)*2*math.Pi) * morphDir

			nlen := math.Sqrt(nx*nx + ny*ny + nz*nz)
			if nlen > 1e-6 {
				nx /= nlen
				ny /= nlen
				nz /= nlen
			}
			// Luminous accent shimmer right at the crest of the 3D morph wave.
			tint = 0.55 * arc
			if fromShape == shapeNet {
				nm, nt := neuralShadeAndTint(triple.net)
				shadeBoost = nm + (1.0-nm)*s
				tint = math.Max(tint, nt*(1-s))
			} else if toShape == shapeNet {
				nm, nt := neuralShadeAndTint(triple.net)
				shadeBoost = 1.0 + (nm-1.0)*s
				tint = math.Max(tint, nt*s)
			}
		}

		// Rotate normal into view space for diffuse + specular shading.
		rnx, rnz := nx*cy+nz*sy, -nx*sy+nz*cy
		rny, rnz := ny*cp-rnz*sp, ny*sp+rnz*cp

		diffuse := math.Max(0, -0.36*rnx-0.48*rny-0.80*rnz)
		rim := math.Pow(math.Max(0, 1-math.Abs(rnz)), 2.5) * 0.24
		specular := math.Pow(diffuse, 10)

		rotX, rotZ := px*cy+pz*sy, -px*sy+pz*cy
		rotZ = py*sp + rotZ*cp
		sweep := math.Pow((1+math.Cos(sweepPhase-rotX*0.20))/2, 3.5)
		depthCue := math.Max(0.72, math.Min(1.15, 1.0-0.05*rotZ))

		baseLight := 46 + 86*diffuse + 48*rim + 72*specular*sweep
		if fromShape == shapeNet && toShape == shapeNet && triple.net.netKind == netKindEdge {
			baseLight = 92 + 34*diffuse
		} else if fromShape == toShape && fromShape != shapeNet {
			// Subtle theme-tinted specular glint gliding across "</>" and "loop".
			tint = math.Max(tint, 0.65*sweep*(0.35+0.65*specular))
		}
		brightness := baseLight * depthCue * shadeBoost * reveal
		plot(px*activeScale, py*activeScale, pz*activeScale, brightness, tint)
	}

	options := Appearance{Brightness: 55}
	if len(appearance) > 0 {
		options = appearance[0]
	}
	if options.Brightness <= 0 {
		options.Brightness = 55
	}
	priR, priG, priB := colorToRGB(options.Primary, 137, 180, 250)
	hiR, hiG, hiB := colorToRGB(options.Highlight, 250, 179, 135)
	brightScale := math.Min(1.0, float64(options.Brightness)/100.0)
	// Accent glow stays vivid even at moderate logo_brightness without blinding.
	accentGain := math.Min(1.0, 0.55+0.45*brightScale)

	var grayTable [24]float64
	for i := range grayTable {
		shade := float64(35+i*165/(len(grayTable)-1)) * float64(options.Brightness) / 100
		if options.Light {
			shade = 255 - shade
		}
		grayTable[i] = shade
	}

	bits := [4][2]rune{{1, 8}, {2, 16}, {4, 32}, {64, 128}}
	lines := make([]string, rows)
	for row := range rows {
		var line strings.Builder
		for col := range cols {
			var mask rune
			brightness, tintSum, count := 0.0, 0.0, 0
			for y := range 4 {
				for x := range 2 {
					idx := (row*4+y)*width + col*2 + x
					value := light[idx]
					if value < 30 {
						continue
					}
					mask |= bits[y][x]
					brightness += value
					tintSum += accent[idx]
					count++
				}
			}
			if mask == 0 {
				line.WriteByte(' ')
				continue
			}
			avgB := brightness / float64(count)
			avgT := math.Max(0, math.Min(1, tintSum/float64(count)))
			shadeIdx := min(len(grayTable)-1, max(0, int((avgB-35)*float64(len(grayTable)-1)/165)))
			g := grayTable[shadeIdx]

			var rOut, gOut, bOut int
			if avgT <= 0.04 {
				v := min(255, max(0, int(g)))
				rOut, gOut, bOut = v, v, v
			} else {
				// Blend primary -> highlight at peak activation intensity.
				hot := math.Max(0, math.Min(1, (avgT-0.55)/0.45))
				tr := (priR*(1-hot) + hiR*hot) * accentGain
				tg := (priG*(1-hot) + hiG*hot) * accentGain
				tb := (priB*(1-hot) + hiB*hot) * accentGain
				rOut = min(255, max(0, int(g*(1-avgT)+tr*avgT)))
				gOut = min(255, max(0, int(g*(1-avgT)+tg*avgT)))
				bOut = min(255, max(0, int(g*(1-avgT)+tb*avgT)))
			}
			startSeq, resetSeq := style.FormatRGBForeground(uint8(rOut), uint8(gOut), uint8(bOut))
			if startSeq != "" {
				line.WriteString(startSeq)
				line.WriteRune(0x2800 + mask)
				line.WriteString(resetSeq)
			} else {
				line.WriteRune(0x2800 + mask)
			}
		}
		lines[row] = line.String()
	}
	return lines
}

const (
	netKindNone uint8 = iota
	netKindNode
	netKindEdge
)

type logoParticle struct {
	x, y, z, nx, ny, nz float64
	netKind             uint8
	netIdx              uint8
	netT                float64
}

type logoMorphTriple struct {
	net  logoParticle
	code logoParticle
	word logoParticle
	wave float64 // normalized [0,1] left-to-right stagger coordinate
}

type neuralNode struct {
	x, y, z float64
	r       float64
	layer   int
	stagger float64
}

type neuralEdge struct {
	from, to int
	stagger  float64
}

// 10 large, immediately recognizable 3D spherical neurons arranged in a clean
// 3 -> 4 -> 3 neural network arched gently forward in 3D space.
var neuralNodes = [10]neuralNode{
	// Layer 0: Input (3 nodes, left)
	{x: -10.0, y: -4.2, z: 1.4, r: 1.04, layer: 0, stagger: 0.00},
	{x: -10.0, y: 0.0, z: 0.8, r: 1.08, layer: 0, stagger: 0.02},
	{x: -10.0, y: 4.2, z: 1.4, r: 1.04, layer: 0, stagger: 0.04},

	// Layer 1: Hidden core (4 nodes, center, arched toward viewer)
	{x: 0.0, y: -5.1, z: -1.1, r: 1.02, layer: 1, stagger: 0.00},
	{x: 0.0, y: -1.7, z: -1.8, r: 1.08, layer: 1, stagger: 0.02},
	{x: 0.0, y: 1.7, z: -1.8, r: 1.08, layer: 1, stagger: 0.03},
	{x: 0.0, y: 5.1, z: -1.1, r: 1.02, layer: 1, stagger: 0.01},

	// Layer 2: Output (3 nodes, right)
	{x: 10.0, y: -4.2, z: 1.4, r: 1.04, layer: 2, stagger: 0.00},
	{x: 10.0, y: 0.0, z: 0.8, r: 1.08, layer: 2, stagger: 0.02},
	{x: 10.0, y: 4.2, z: 1.4, r: 1.04, layer: 2, stagger: 0.04},
}

// 16 crisp, non-cluttered straight neural links (8 fan-out + 8 fan-in).
var neuralEdges = [16]neuralEdge{
	// Layer 0 -> Layer 1
	{0, 3, 0.00}, {0, 4, 0.02},
	{1, 3, 0.03}, {1, 4, 0.01}, {1, 5, 0.02}, {1, 6, 0.04},
	{2, 5, 0.01}, {2, 6, 0.03},
	// Layer 1 -> Layer 2
	{3, 7, 0.00}, {3, 8, 0.03},
	{4, 7, 0.02}, {4, 8, 0.01},
	{5, 8, 0.02}, {5, 9, 0.01},
	{6, 8, 0.04}, {6, 9, 0.02},
}

// Precompute matched 3D particle clouds for:
//  1. Clean 3-4-3 3D neural network (10 large sculpted spheres + 16 straight signal wires)
//  2. 3D "</>" code sign (beveled angle brackets and forward slash)
//  3. 3D "loop" wordmark
//
// All three clouds are spatially sorted and resampled 1-to-1 for fluid 3D morphs.
var logoMorphTriples = func() []logoMorphTriple {
	capsule := func(x, y, ax, ay, bx, by, r float64) float64 {
		dx, dy := bx-ax, by-ay
		t := math.Max(0, math.Min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
		return math.Hypot(x-ax-t*dx, y-ay-t*dy) - r
	}

	// --- 1. Clean 3-4-3 3D Neural Network ---
	var rawNet []logoParticle
	ga := math.Pi * (3 - math.Sqrt(5))
	for idx, n := range neuralNodes {
		const samples = 290
		for i := range samples {
			y := 1 - (2*float64(i)+1)/float64(samples)
			rad := math.Sqrt(math.Max(0, 1-y*y))
			theta := ga * float64(i)
			nx := math.Cos(theta) * rad
			ny := y
			nz := math.Sin(theta) * rad
			rawNet = append(rawNet, logoParticle{
				x:       n.x + n.r*nx,
				y:       n.y + n.r*ny,
				z:       n.z + n.r*nz,
				nx:      nx,
				ny:      ny,
				nz:      nz,
				netKind: netKindNode,
				netIdx:  uint8(idx),
				netT:    float64(n.layer) / 2.0,
			})
		}
	}

	// Sample each straight wire cleanly from sphere surface to sphere surface.
	const edgeSteps = 68
	const edgeRing = 2
	const edgeRadius = 0.04
	for idx, e := range neuralEdges {
		a := neuralNodes[e.from]
		b := neuralNodes[e.to]
		dx, dy, dz := b.x-a.x, b.y-a.y, b.z-a.z
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dist < 1e-6 {
			continue
		}
		tx, ty := dx/dist, dy/dist
		nx0, ny0 := -ty, tx
		nlen := math.Hypot(nx0, ny0)
		if nlen > 1e-6 {
			nx0 /= nlen
			ny0 /= nlen
		} else {
			nx0, ny0 = 0, 1
		}
		startFrac := (a.r + 0.15) / dist
		endFrac := 1.0 - (b.r+0.15)/dist
		for step := range edgeSteps {
			t := float64(step) / float64(edgeSteps-1)
			f := startFrac + t*(endFrac-startFrac)
			px := a.x + dx*f
			py := a.y + dy*f
			pz := a.z + dz*f
			for r := range edgeRing {
				sign := 1.0
				if r == 1 {
					sign = -1.0
				}
				rawNet = append(rawNet, logoParticle{
					x:       px + sign*edgeRadius*nx0,
					y:       py + sign*edgeRadius*ny0,
					z:       pz,
					nx:      sign * nx0,
					ny:      sign * ny0,
					nz:      -0.8,
					netKind: netKindEdge,
					netIdx:  uint8(idx),
					netT:    t,
				})
			}
		}
	}

	// --- 2. 3D "</>" Code Sign ---
	const codeRadius = 0.52
	codeDist := func(x, y float64) float64 {
		leftTop := capsule(x, y, -3.85, -2.65, -8.15, 0.0, codeRadius)
		leftBot := capsule(x, y, -8.15, 0.0, -3.85, 2.65, codeRadius)
		slash := capsule(x, y, 1.30, -3.50, -1.30, 3.50, codeRadius)
		rightTop := capsule(x, y, 3.85, -2.65, 8.15, 0.0, codeRadius)
		rightBot := capsule(x, y, 8.15, 0.0, 3.85, 2.65, codeRadius)
		left := math.Min(leftTop, leftBot)
		right := math.Min(rightTop, rightBot)
		return math.Min(slash, math.Min(left, right))
	}
	const codeDepth = 0.70
	var rawCode []logoParticle
	for iy := 0; iy <= 140; iy++ {
		y := -4.3 + float64(iy)*0.062
		for ix := 0; ix <= 290; ix++ {
			x := -8.9 + float64(ix)*0.062
			d := codeDist(x, y)
			if d > 0 {
				continue
			}
			gx := codeDist(x+0.002, y) - codeDist(x-0.002, y)
			gy := codeDist(x, y+0.002) - codeDist(x, y-0.002)
			gl := math.Hypot(gx, gy)
			radial := math.Max(0, math.Min(1, (codeRadius+d)/codeRadius))
			nx, ny := 0.0, 0.0
			if gl > 0 {
				nx, ny = radial*gx/gl, radial*gy/gl
			}
			nz := -math.Sqrt(math.Max(0, 1-radial*radial))
			rawCode = append(rawCode, logoParticle{
				x: x, y: y, z: codeDepth * nz,
				nx: nx, ny: ny, nz: nz,
			})
			if (ix+iy)%3 == 0 && nz < -0.15 {
				rawCode = append(rawCode, logoParticle{
					x: x, y: y, z: -codeDepth * nz,
					nx: nx, ny: ny, nz: -nz,
				})
			}
		}
	}

	// --- 3. 3D "loop" Wordmark ---
	wordDist := func(glyph int, x, y float64) float64 {
		switch glyph {
		case 0:
			return capsule(x, y, 2, 0.2, 2, 6, 0.45)
		case 1, 2:
			return math.Abs(math.Hypot(x-2.4, y-4.2)-1.9) - 0.45
		default:
			stem := capsule(x, y, 0.6, 2.3, 0.6, 8, 0.45)
			bowl := math.Abs(math.Hypot(x-2.3, y-4.2)-1.7) - 0.45
			return math.Min(stem, bowl)
		}
	}
	advances := [4]float64{0, 3.3, 8.7, 14.1}
	const wordCenter = 10.1
	const wordDepth = 0.54
	var rawWord []logoParticle
	for glyph := range 4 {
		for iy := 0; iy <= 170; iy++ {
			y := float64(iy)*0.053 - 0.6
			for ix := 0; ix <= 104; ix++ {
				x := float64(ix)*0.053 - 0.2
				d := wordDist(glyph, x, y)
				if d > 0 {
					continue
				}
				gx := wordDist(glyph, x+0.001, y) - wordDist(glyph, x-0.001, y)
				gy := wordDist(glyph, x, y+0.001) - wordDist(glyph, x, y-0.001)
				gl := math.Hypot(gx, gy)
				radial := math.Max(0, math.Min(1, (0.45+d)/0.45))
				nx, ny := 0.0, 0.0
				if gl > 0 {
					nx, ny = radial*gx/gl, radial*gy/gl
				}
				nz := -math.Sqrt(math.Max(0, 1-radial*radial))
				wx := advances[glyph] + x - wordCenter
				wy := y - 4.0
				rawWord = append(rawWord, logoParticle{
					x: wx, y: wy, z: wordDepth * nz,
					nx: nx, ny: ny, nz: nz,
				})
				if (ix+iy)%3 == 0 && nz < -0.15 {
					rawWord = append(rawWord, logoParticle{
						x: wx, y: wy, z: -wordDepth * nz,
						nx: nx, ny: ny, nz: -nz,
					})
				}
			}
		}
	}

	// Sort all three clouds left-to-right (with gentle vertical cohesion) and pair 1-to-1.
	spatialKey := func(p logoParticle) float64 {
		return p.x + 0.14*p.y
	}
	sort.Slice(rawNet, func(i, j int) bool { return spatialKey(rawNet[i]) < spatialKey(rawNet[j]) })
	sort.Slice(rawCode, func(i, j int) bool { return spatialKey(rawCode[i]) < spatialKey(rawCode[j]) })
	sort.Slice(rawWord, func(i, j int) bool { return spatialKey(rawWord[i]) < spatialKey(rawWord[j]) })

	const count = 5600
	triples := make([]logoMorphTriple, count)
	minX, maxX := rawWord[0].x, rawWord[len(rawWord)-1].x
	spanX := math.Max(1e-6, maxX-minX)
	pick := func(src []logoParticle, i int) logoParticle {
		idx := (i * len(src)) / count
		if idx >= len(src) {
			idx = len(src) - 1
		}
		return src[idx]
	}
	for i := range count {
		n := pick(rawNet, i)
		c := pick(rawCode, i)
		w := pick(rawWord, i)
		wave := math.Max(0, math.Min(1, (w.x-minX)/spanX))
		triples[i] = logoMorphTriple{
			net:  n,
			code: c,
			word: w,
			wave: wave,
		}
	}
	return triples
}()
