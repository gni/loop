package logo

import (
	"math"
	"sync"

	"loop/pkg/ui/style"
)

var canvasPool = sync.Pool{
	New: func() any {
		return &frameCanvas{
			ansiBuf: make([]byte, 0, 32),
		}
	},
}

// Render draws a single frame of the real-time 3D bio-electric Braille startup
// animation (3D Neural Constellation <-> </> Code Prism <-> 3D "loop" Wordmark)
// sized to cols x rows terminal cells.
func Render(cols, rows, frame int, appearance ...Appearance) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	options := Appearance{Brightness: 55}
	if len(appearance) > 0 {
		options = appearance[0]
	}

	width, height := cols*2, rows*4
	canvas := canvasPool.Get().(*frameCanvas)
	defer canvasPool.Put(canvas)

	canvas.reset(width, height)
	st := &canvas.st
	buildSceneState(st, width, height, frame, options)

	// 1. Rasterize analytical 3D solid glass surfaces (with phase-transition clip during morphs).
	renderShapeGeometry(st, canvas, st.fromShape, true)
	if st.morph > 0 && st.toShape != st.fromShape {
		renderShapeGeometry(st, canvas, st.toShape, false)
	}

	// 2. Render live 3D electromagnetic arcs, shockwaves, infinity circuit, and morph vortex.
	if st.morph > 0 && st.toShape != st.fromShape {
		renderMorphVortex(st, canvas)
	} else {
		switch st.fromShape {
		case shapeNeural:
			renderNeuralElectricity(st, canvas)
		case shapeCode:
			renderCodeElectricity(st, canvas)
		default:
			renderWordElectricity(st, canvas)
		}
	}

	return composeBrailleRows(st, canvas, cols, rows)
}

func renderShapeGeometry(st *sceneState, c *frameCanvas, shape int, isFrom bool) {
	switch shape {
	case shapeNeural:
		rasterizeNeuralNodes(st, c, isFrom)
		rasterizeNeuralConduits(st, c, isFrom)
	case shapeCode:
		rasterizeCodePrism(st, c, isFrom)
	default:
		rasterizeWordmark(st, c, isFrom)
	}
}

// softHighlightCurve maps unbounded linear HDR luminance into [0, 1] while
// preserving incandescent high-voltage brilliance instead of crushing peaks.
func softHighlightCurve(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return clamp01(x / (1.0 + 0.48*x))
}

// toneMapDark applies luminance-preserving HDR tone mapping so high-voltage
// electric arcs keep vivid jewel-toned theme chroma while ONLY the hottest
// peak-voltage cores bloom cleanly toward incandescent white.
func toneMapDark(c rgbLinear, gain float64) rgbLinear {
	r := c.r * gain
	g := c.g * gain
	b := c.b * gain
	l := 0.2126*r + 0.7152*g + 0.0722*b
	if l <= 1e-6 {
		return rgbLinear{}
	}
	mappedL := softHighlightCurve(l)
	sc := mappedL / l
	out := rgbLinear{r: r * sc, g: g * sc, b: b * sc}
	// Preserve hue for mid/bright plasma; allow subtle incandescent highlight rolloff at extreme peaks.
	peak := math.Max(out.r, math.Max(out.g, out.b))
	if peak > 1.0 {
		// Softly bleed excess channel energy into luminance rather than hard clipping.
		excess := clamp01((peak - 1.0) * 0.55)
		out = out.mul(1.0/peak).mix(rgbLinear{1.0, 1.0, 1.0}, excess*0.45)
	}
	return rgbLinear{r: clamp01(out.r), g: clamp01(out.g), b: clamp01(out.b)}
}

// quantizeChannel snaps 8-bit RGB channels to a perceptual 8-unit step (~32
// levels/channel) so adjacent Braille cells run-length compress cleanly without
// visible banding, keeping terminal overlay frames compact and flicker-free.
func quantizeChannel(v uint8) uint8 {
	q := (int(v) + 4) / 8 * 8
	if q > 255 {
		return 255
	}
	return uint8(q)
}

// composeBrailleRows converts the linear-light solid + additive plasma subpixel
// framebuffer into compact 2x4 Braille rows using energy-dominant cell color
// weighting so incandescent electric arcs cut crisply across 3D glass surfaces.
func composeBrailleRows(st *sceneState, c *frameCanvas, cols, rows int) []string {
	width := c.width
	pal := &st.pal
	lines := make([]string, rows)

	c.ansiBuf = c.ansiBuf[:0]
	_, hasColor := style.AppendRGBForeground(c.ansiBuf, 128, 128, 128)

	for row := range rows {
		c.line.Reset()
		c.line.Grow(cols * 6)

		colorActive := false
		var lastR, lastG, lastB uint8
		pendingSpaces := 0

		for col := range cols {
			var mask rune
			var sumR, sumG, sumB float64
			var wSum float64
			var maxSolid float64
			var maxElec float64

			baseY := row * 4
			baseX := col * 2
			for dy := range 4 {
				sy := baseY + dy
				rowIdx := sy * width
				bayerRow := &bayer4x4[sy&3]
				for dx := range 2 {
					sx := baseX + dx
					idx := rowIdx + sx

					sCov := c.solidCov[idx]
					eCov := c.elecCov[idx]
					if sCov <= 0.01 && eCov <= 0.05 {
						continue
					}

					lit := false
					if sCov >= 0.34 || eCov >= 0.24 {
						lit = true
					} else if eCov > 0.08 && eCov > bayerRow[sx&3]*0.32 {
						lit = true
					}
					if !lit {
						continue
					}

					if sCov > maxSolid {
						maxSolid = sCov
					}
					if eCov > maxElec {
						maxElec = eCov
					}

					mask |= brailleBits[dy][dx]

					var r, g, b float64
					if pal.light {
						// On light canvas, electric plasma acts as subtractive/vivid ink
						// overwriting dormant slate rather than adding toward white!
						eAlpha := clamp01(eCov)
						var eCol rgbLinear
						if eCov > 1e-4 {
							invE := 1.0 / eCov
							eCol = rgbLinear{c.elecR[idx] * invE, c.elecG[idx] * invE, c.elecB[idx] * invE}
						}
						if sCov > 0.01 {
							sCol := rgbLinear{c.surfR[idx], c.surfG[idx], c.surfB[idx]}
							blended := sCol.mix(eCol, eAlpha)
							r, g, b = blended.r, blended.g, blended.b
						} else {
							r, g, b = eCol.r, eCol.g, eCol.b
						}
					} else {
						// Dark canvas: physical additive linear-light plasma over 3D glass.
						r = c.surfR[idx]*sCov + c.elecR[idx]
						g = c.surfG[idx]*sCov + c.elecG[idx]
						b = c.surfB[idx]*sCov + c.elecB[idx]
					}

					// Energy-dominant weighting ensures electric sparks and bolts
					// punch cleanly through neighboring dim glass dots in the same cell.
					en := math.Max(c.surfEnergy[idx]*sCov, clamp01(eCov*1.15))
					w := 0.20 + 3.0*en*en

					sumR += r * w
					sumG += g * w
					sumB += b * w
					wSum += w
				}
			}

			if mask == 0 || wSum <= 1e-9 {
				pendingSpaces++
				continue
			}

			invW := 1.0 / wSum
			rad := rgbLinear{
				r: sumR * invW,
				g: sumG * invW,
				b: sumB * invW,
			}

			var outR, outG, outB uint8
			if pal.light {
				// On light themes, keep high-voltage ink deep, vivid, and crisp against paper.
				density := clamp01(math.Max(maxSolid, clamp01(maxElec*1.35)))
				contrast := clamp01(0.66 + 0.34*pal.brightGain)
				ink := rgbLinear{
					r: clamp01(rad.r),
					g: clamp01(rad.g),
					b: clamp01(rad.b),
				}
				blended := pal.background.mix(ink, density*contrast)
				if maxSolid < 0.20 && maxElec < 0.20 && math.Abs(pal.bgLum-blended.lum()) < 0.03 {
					pendingSpaces++
					continue
				}
				outR = quantizeChannel(linearToSRGBByte(blended.r))
				outG = quantizeChannel(linearToSRGBByte(blended.g))
				outB = quantizeChannel(linearToSRGBByte(blended.b))
			} else {
				// On dark themes, use luminance-preserving HDR tone mapping so arcs stay
				// richly saturated in theme hues while blazing incandescent at peak cores.
				gain := pal.brightGain * (1.14 + 0.36*clamp01(maxElec))
				toned := toneMapDark(rad, gain)
				if maxSolid < 0.20 && maxElec < 0.16 && toned.lum() < 0.020 {
					pendingSpaces++
					continue
				}
				outR = quantizeChannel(linearToSRGBByte(toned.r))
				outG = quantizeChannel(linearToSRGBByte(toned.g))
				outB = quantizeChannel(linearToSRGBByte(toned.b))
			}

			for pendingSpaces > 0 {
				c.line.WriteByte(' ')
				pendingSpaces--
			}

			if hasColor && (!colorActive || outR != lastR || outG != lastG || outB != lastB) {
				c.ansiBuf = c.ansiBuf[:0]
				var ok bool
				c.ansiBuf, ok = style.AppendRGBForeground(c.ansiBuf, outR, outG, outB)
				if ok {
					c.line.Write(c.ansiBuf)
					colorActive = true
					lastR, lastG, lastB = outR, outG, outB
				}
			}
			c.line.WriteRune(0x2800 + mask)
		}

		if colorActive {
			c.line.WriteString("\x1b[0m")
		}
		lines[row] = c.line.String()
	}
	return lines
}
