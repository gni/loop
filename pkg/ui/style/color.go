package style

import (
	"image/color"
	"strconv"
)

type colorVal struct {
	R, G, B uint8
}

func (c colorVal) RGBA() (r, g, b, a uint32) {
	r = uint32(c.R) * 0x101
	g = uint32(c.G) * 0x101
	b = uint32(c.B) * 0x101
	a = 0xffff
	return
}

func Color(hex string) colorVal {
	if len(hex) > 0 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) == 6 {
		r, _ := strconv.ParseUint(hex[0:2], 16, 8)
		g, _ := strconv.ParseUint(hex[2:4], 16, 8)
		b, _ := strconv.ParseUint(hex[4:6], 16, 8)
		return colorVal{R: uint8(r), G: uint8(g), B: uint8(b)}
	}
	return colorVal{255, 255, 255}
}

// BlendColor linearly interpolates between colors a (t=0) and b (t=1).
func BlendColor(a, b color.Color, t float64) color.Color {
	ca, cb := toColorVal(a), toColorVal(b)
	if ca == nil {
		return b
	}
	if cb == nil {
		return a
	}
	if t <= 0 {
		return *ca
	}
	if t >= 1 {
		return *cb
	}
	return colorVal{
		R: uint8(float64(ca.R) + t*(float64(cb.R)-float64(ca.R)) + 0.5),
		G: uint8(float64(ca.G) + t*(float64(cb.G)-float64(ca.G)) + 0.5),
		B: uint8(float64(ca.B) + t*(float64(cb.B)-float64(ca.B)) + 0.5),
	}
}
