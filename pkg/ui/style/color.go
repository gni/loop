package style

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
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

// Hex formats a colorVal as uppercase #RRGGBB.
func (c colorVal) Hex() string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// ColorHex formats any color.Color as #RRGGBB (or "" if nil).
func ColorHex(c color.Color) string {
	if cv := toColorVal(c); cv != nil {
		return cv.Hex()
	}
	return ""
}

// IsValidHexColor reports whether s is a valid #RGB or #RRGGBB hex color.
func IsValidHexColor(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) != 3 && len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// NormalizeHexColor converts #RGB or #RRGGBB (with or without leading '#') to #RRGGBB.
func NormalizeHexColor(s string) (string, bool) {
	if !IsValidHexColor(s) {
		return "", false
	}
	return Color(strings.TrimSpace(s)).Hex(), true
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
