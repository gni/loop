package logo

import (
	"image/color"
	"math"
	"strings"
	"time"
)

const (
	// FrameInterval targets 25 FPS (40ms) for silky electric crackle,
	// shockwave propagation, and fluid 3D vortex morphogenesis.
	FrameInterval = 40 * time.Millisecond

	// EntranceFrames boots the 3D neural constellation from frame 0 with an
	// immediate ignition cascade (~0.96s, zero blank startup delay).
	EntranceFrames = 24

	// Seamless 3-stage cinematic loop (~20.6s cycle after entrance at 25 FPS):
	//   [0..24)    Immediate luminous boot + Layer 0->1->2 ignition (~0.96s)
	//   [24..214)  Stage 1: Bio-Electric 3D Neural Constellation (~7.6s)
	//              Forward Pass -> Hidden Cross-Talk -> Backprop Echo -> Core Convergence
	//   [214..252) Morph 1: Electromagnetic Vortex + Tesla Tethers -> </> Code Prism (~1.52s)
	//   [252..356) Stage 2: Electrified 3D </> Code Prism with Air-Gap Arcs (~4.16s)
	//   [356..394) Morph 2: Electromagnetic Vortex + Tesla Tethers -> 3D "loop" (~1.52s)
	//   [394..502) Stage 3: Electrified 3D "loop" Wordmark with Infinity Circuit (~4.32s)
	//   [502..540) Morph 3: Electromagnetic Vortex + Tesla Tethers -> Neural (~1.52s)
	stageNeuralEnd  = 214
	morphNetToCode  = 252
	stageCodeEnd    = 356
	morphCodeToWord = 394
	stageWordEnd    = 502

	// CycleFrames is the total frame count before wrapping seamlessly to EntranceFrames.
	CycleFrames = 540

	// ReducedFrame captures an iconic mid-cascade still of the 3D neural network
	// with live crackling synaptic arcs and glowing core shockwaves.
	ReducedFrame = 66
)

const (
	shapeNeural = 0
	shapeCode   = 1
	shapeWord   = 2
)

const (
	numNeuralNodes   = 10
	numNeuralEdges   = 16
	numHiddenBridges = 3
	numCodeBars      = 5
	numCodeAirArcs   = 2
	numWordGlyphs    = 4
)

// Appearance configures contrast and theme palette harmony for the 3D Braille
// startup animation. Zero-valued colors fall back to refined defaults.
type Appearance struct {
	Brightness int
	Light      bool
	Primary    color.Color
	Secondary  color.Color
	Highlight  color.Color
	TextMuted  color.Color
	Background color.Color
}

type vec3 struct {
	x, y, z float64
}

func (v vec3) add(o vec3) vec3    { return vec3{v.x + o.x, v.y + o.y, v.z + o.z} }
func (v vec3) sub(o vec3) vec3    { return vec3{v.x - o.x, v.y - o.y, v.z - o.z} }
func (v vec3) mul(s float64) vec3 { return vec3{v.x * s, v.y * s, v.z * s} }
func (v vec3) dot(o vec3) float64 { return v.x*o.x + v.y*o.y + v.z*o.z }
func (v vec3) lenSq() float64     { return v.x*v.x + v.y*v.y + v.z*v.z }
func (v vec3) length() float64    { return math.Sqrt(v.x*v.x + v.y*v.y + v.z*v.z) }
func (v vec3) mix(o vec3, t float64) vec3 {
	u := 1.0 - t
	return vec3{v.x*u + o.x*t, v.y*u + o.y*t, v.z*u + o.z*t}
}
func (v vec3) cross(o vec3) vec3 {
	return vec3{
		x: v.y*o.z - v.z*o.y,
		y: v.z*o.x - v.x*o.z,
		z: v.x*o.y - v.y*o.x,
	}
}
func (v vec3) normalize() vec3 {
	l := v.length()
	if l < 1e-9 {
		return vec3{0, 0, -1}
	}
	inv := 1.0 / l
	return vec3{v.x * inv, v.y * inv, v.z * inv}
}

type rgbLinear struct {
	r, g, b float64
}

func (c rgbLinear) add(o rgbLinear) rgbLinear { return rgbLinear{c.r + o.r, c.g + o.g, c.b + o.b} }
func (c rgbLinear) mul(s float64) rgbLinear   { return rgbLinear{c.r * s, c.g * s, c.b * s} }
func (c rgbLinear) lum() float64              { return 0.2126*c.r + 0.7152*c.g + 0.0722*c.b }
func (c rgbLinear) mix(o rgbLinear, t float64) rgbLinear {
	u := 1.0 - t
	return rgbLinear{c.r*u + o.r*t, c.g*u + o.g*t, c.b*u + o.b*t}
}

type neuralNodeSpec struct {
	base    vec3
	radius  float64
	layer   int
	stagger float64
	phase   float64
}

type neuralEdgeSpec struct {
	from    int
	to      int
	archY   float64
	archZ   float64
	stagger float64
}

// Ten sculpted 3D crystalline neural cells arranged in an arched 3 -> 4 -> 3
// constellation with clear spatial separation and depth curvature.
var neuralNodes = [numNeuralNodes]neuralNodeSpec{
	// Layer 0: Input (left, receding slightly into depth)
	{base: vec3{-9.6, -4.15, 1.55}, radius: 1.04, layer: 0, stagger: 0.00, phase: 0.3},
	{base: vec3{-9.8, 0.00, 0.95}, radius: 1.12, layer: 0, stagger: 0.018, phase: 1.5},
	{base: vec3{-9.6, 4.15, 1.55}, radius: 1.04, layer: 0, stagger: 0.036, phase: 2.7},

	// Layer 1: Hidden core (center, arched forward onto focal plane)
	{base: vec3{0.0, -5.00, -0.75}, radius: 1.02, layer: 1, stagger: 0.00, phase: 0.9},
	{base: vec3{0.0, -1.72, -1.85}, radius: 1.16, layer: 1, stagger: 0.015, phase: 2.2},
	{base: vec3{0.0, 1.72, -1.85}, radius: 1.16, layer: 1, stagger: 0.028, phase: 3.5},
	{base: vec3{0.0, 5.00, -0.75}, radius: 1.02, layer: 1, stagger: 0.012, phase: 4.8},

	// Layer 2: Output (right, symmetric depth)
	{base: vec3{9.6, -4.15, 1.55}, radius: 1.04, layer: 2, stagger: 0.00, phase: 1.8},
	{base: vec3{9.8, 0.00, 0.95}, radius: 1.12, layer: 2, stagger: 0.018, phase: 3.1},
	{base: vec3{9.6, 4.15, 1.55}, radius: 1.04, layer: 2, stagger: 0.036, phase: 4.9},
}

// Sixteen arched 3D quadratic Bezier synaptic pathways (8 fan-out + 8 fan-in).
var neuralEdges = [numNeuralEdges]neuralEdgeSpec{
	{from: 0, to: 3, archY: -0.45, archZ: -0.42, stagger: 0.00},
	{from: 0, to: 4, archY: -0.20, archZ: 0.45, stagger: 0.018},
	{from: 1, to: 3, archY: -0.32, archZ: -0.48, stagger: 0.025},
	{from: 1, to: 4, archY: -0.16, archZ: -0.30, stagger: 0.008},
	{from: 1, to: 5, archY: 0.16, archZ: -0.30, stagger: 0.016},
	{from: 1, to: 6, archY: 0.32, archZ: -0.48, stagger: 0.032},
	{from: 2, to: 5, archY: 0.20, archZ: 0.45, stagger: 0.012},
	{from: 2, to: 6, archY: 0.45, archZ: -0.42, stagger: 0.026},

	{from: 3, to: 7, archY: -0.45, archZ: -0.42, stagger: 0.00},
	{from: 3, to: 8, archY: -0.32, archZ: -0.48, stagger: 0.024},
	{from: 4, to: 7, archY: -0.20, archZ: 0.45, stagger: 0.016},
	{from: 4, to: 8, archY: -0.16, archZ: -0.30, stagger: 0.008},
	{from: 5, to: 8, archY: 0.16, archZ: -0.30, stagger: 0.015},
	{from: 5, to: 9, archY: 0.20, archZ: 0.45, stagger: 0.010},
	{from: 6, to: 8, archY: 0.32, archZ: -0.48, stagger: 0.030},
	{from: 6, to: 9, archY: 0.45, archZ: -0.42, stagger: 0.020},
}

// Vertical inter-hidden resonance bridges allowing live electric cross-talk
// between the 4 central core cells during peak neural excitation.
var hiddenBridges = [numHiddenBridges][2]int{
	{3, 4},
	{4, 5},
	{5, 6},
}

type nodePose struct {
	center     vec3
	radius     float64
	baseRadius float64
	energy     float64 // [0..1] total membrane & nucleus excitation
	coreHot    float64 // [0..1] incandescent core flash intensity
	harmonic   float64 // 0 = Primary forward wave, 1 = Highlight backprop/core wave
	shockProg  float64 // [0..1] expanding 3D electromagnetic shockwave ring progress
	shockAmp   float64 // [0..1] shockwave brightness
	phase      float64
}

type edgePose struct {
	a, ctrl, b vec3
	head       float64 // normalized [0..1] bolt head position along axon
	intensity  float64 // [0..1] live voltage
	harmonic   float64 // 0 = Primary, 1 = Highlight
	reverse    bool    // true during backprop echo (b -> a)
	active     bool
}

type crossArcPose struct {
	from, to  int
	intensity float64
	harmonic  float64
	active    bool
}

type codeBarPose struct {
	a, b       vec3
	radius     float64
	chargeT    float64 // traveling pulse position along bar [0..1]
	chargeAmp  float64 // traveling pulse strength
	baseEnergy float64
	harmonic   float64
	group      int // 0 = '<', 1 = '/', 2 = '>'
}

type codeAirArc struct {
	a, b, ctrl vec3
	head       float64
	intensity  float64
	harmonic   float64
	active     bool
}

type wordGlyphPose struct {
	kind     uint8 // 0 = 'l' capsule, 1 = 'o' torus, 2 = 'p' capsule + torus
	a, b     vec3
	center   vec3
	majorR   float64
	minorR   float64
	energy   float64
	coreHot  float64
	harmonic float64
	pulseU   float64 // parametric position of traveling charge
	pulseAmp float64
}

type cameraState struct {
	pos        vec3
	right      vec3
	up         vec3
	forward    vec3
	focalPx    float64
	invFocalPx float64
	focalDist  float64
	halfW      float64
	halfH      float64
	scale      float64
}

type paletteLinear struct {
	shellDark  rgbLinear
	shellMid   rgbLinear
	primary    rgbLinear
	secondary  rgbLinear
	highlight  rgbLinear
	coreHot    rgbLinear
	background rgbLinear
	bgLum      float64
	brightGain float64
	light      bool
}

type sceneState struct {
	frame     int
	fromShape int
	toShape   int
	morph     float64
	morphDir  float64
	reveal    float64
	cycleU    float64
	stageU    float64
	timeSec   float64

	cam       cameraState
	keyLight  vec3
	fillLight vec3
	rimLight  vec3
	pal       paletteLinear

	nodes     [numNeuralNodes]nodePose
	edges     [numNeuralEdges]edgePose
	crossArcs [numHiddenBridges]crossArcPose
	bars      [numCodeBars]codeBarPose
	codeArcs  [numCodeAirArcs]codeAirArc
	word      [numWordGlyphs]wordGlyphPose

	// Stage 3 ("loop") figure-eight infinity accelerator state.
	loopCircuitPhase float64
	loopCircuitAmp   float64

	width  int
	height int
}

type frameCanvas struct {
	st         sceneState
	width      int
	height     int
	depth      []float64
	solidCov   []float64
	surfR      []float64
	surfG      []float64
	surfB      []float64
	surfEnergy []float64
	elecCov    []float64
	elecR      []float64
	elecG      []float64
	elecB      []float64
	ansiBuf    []byte
	line       strings.Builder
}

func (c *frameCanvas) reset(width, height int) {
	c.width = width
	c.height = height
	n := width * height
	if cap(c.depth) < n {
		c.depth = make([]float64, n)
		c.solidCov = make([]float64, n)
		c.surfR = make([]float64, n)
		c.surfG = make([]float64, n)
		c.surfB = make([]float64, n)
		c.surfEnergy = make([]float64, n)
		c.elecCov = make([]float64, n)
		c.elecR = make([]float64, n)
		c.elecG = make([]float64, n)
		c.elecB = make([]float64, n)
	} else {
		c.depth = c.depth[:n]
		c.solidCov = c.solidCov[:n]
		c.surfR = c.surfR[:n]
		c.surfG = c.surfG[:n]
		c.surfB = c.surfB[:n]
		c.surfEnergy = c.surfEnergy[:n]
		c.elecCov = c.elecCov[:n]
		c.elecR = c.elecR[:n]
		c.elecG = c.elecG[:n]
		c.elecB = c.elecB[:n]
		clear(c.solidCov)
		clear(c.surfR)
		clear(c.surfG)
		clear(c.surfB)
		clear(c.surfEnergy)
		clear(c.elecCov)
		clear(c.elecR)
		clear(c.elecG)
		clear(c.elecB)
	}
	for i := range c.depth {
		c.depth[i] = 1e9
	}
}

var brailleBits = [4][2]rune{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// Structured 4x4 Bayer matrix used strictly for outer pinpoint electric corona
// sparkle (never applied to solid 3D silhouettes or bolt cores).
var bayer4x4 = [4][4]float64{
	{0.0625, 0.5625, 0.1875, 0.6875},
	{0.8125, 0.3125, 0.9375, 0.4375},
	{0.2500, 0.7500, 0.1250, 0.6250},
	{0.9900, 0.5000, 0.8750, 0.3750},
}

func clamp01(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
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

func evalQuadBezier(a, ctrl, b vec3, t float64) vec3 {
	u := 1.0 - t
	w0 := u * u
	w1 := 2.0 * u * t
	w2 := t * t
	return vec3{
		x: a.x*w0 + ctrl.x*w1 + b.x*w2,
		y: a.y*w0 + ctrl.y*w1 + b.y*w2,
		z: a.z*w0 + ctrl.z*w1 + b.z*w2,
	}
}

func evalQuadBezierTangent(a, ctrl, b vec3, t float64) vec3 {
	u := 1.0 - t
	return vec3{
		x: 2.0*u*(ctrl.x-a.x) + 2.0*t*(b.x-ctrl.x),
		y: 2.0*u*(ctrl.y-a.y) + 2.0*t*(b.y-ctrl.y),
		z: 2.0*u*(ctrl.z-a.z) + 2.0*t*(b.z-ctrl.z),
	}.normalize()
}

// hashU32 is a fast, pure deterministic avalanche hash (zero global RNG state).
func hashU32(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x7feb352d
	x ^= x >> 15
	x *= 0x846ca68b
	x ^= x >> 16
	return x
}

// hashSigned returns a deterministic float in [-1, 1].
func hashSigned(seed uint32) float64 {
	h := hashU32(seed)
	return (float64(h&0xFFFF)/32767.5 - 1.0)
}

// hashUnit returns a deterministic float in [0, 1].
func hashUnit(seed uint32) float64 {
	return float64(hashU32(seed)&0xFFFF) / 65535.0
}
