package interactive

import (
	"strconv"
	"strings"
)

// ParseMouseEvent decodes SGR (\x1b[<b;x;yM/m) and X10 (\x1b[M + 3 bytes) mouse reports.
// Returned coordinates x, y are 1-based terminal columns and rows.
func ParseMouseEvent(key []byte) (btn, x, y int, release, ok bool) {
	if len(key) >= 6 && key[0] == 0x1b && key[1] == '[' && key[2] == '<' {
		last := key[len(key)-1]
		if last != 'M' && last != 'm' {
			return 0, 0, 0, false, false
		}
		parts := strings.Split(string(key[3:len(key)-1]), ";")
		if len(parts) != 3 {
			return 0, 0, 0, false, false
		}
		b, err1 := strconv.Atoi(parts[0])
		cx, err2 := strconv.Atoi(parts[1])
		cy, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, 0, 0, false, false
		}
		return b, cx, cy, last == 'm', true
	}
	if len(key) == 6 && key[0] == 0x1b && key[1] == '[' && key[2] == 'M' && key[3] >= 32 {
		b := int(key[3] - 32)
		cx := int(key[4] - 32)
		cy := int(key[5] - 32)
		return b, cx, cy, b&0xc3 == 3, true
	}
	return 0, 0, 0, false, false
}

// MouseWheelDelta returns -1 for scroll wheel up, +1 for scroll wheel down, or 0 otherwise.
func MouseWheelDelta(key []byte) int {
	btn, _, _, release, ok := ParseMouseEvent(key)
	if !ok || release || btn&32 != 0 {
		return 0
	}
	switch btn & 0xc3 {
	case 64:
		return -1
	case 65:
		return 1
	default:
		return 0
	}
}

// MouseLeftClick returns the 1-based (x, y) coordinates for a primary button press.
func MouseLeftClick(key []byte) (int, int, bool) {
	btn, x, y, release, ok := ParseMouseEvent(key)
	if !ok || release || btn&32 != 0 || btn&0xc3 != 0 {
		return 0, 0, false
	}
	return x, y, true
}
