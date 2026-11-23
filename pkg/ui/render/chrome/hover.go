package chrome

// HoverTarget identifies an interactive TUI chrome element currently under the mouse pointer.
type HoverTarget uint8

const (
	HoverNone HoverTarget = iota
	HoverCommandsButton
	HoverThemeButton
	HoverAutoBadge
	HoverReasoningBadge
	HoverGitBranch
	HoverCollapseBadge
)

// Hitbox defines a 1-based terminal row and inclusive [StartCol, EndCol] column range.
type Hitbox struct {
	Target   HoverTarget
	Row      int
	StartCol int
	EndCol   int
}

// HitTest returns the HoverTarget whose hitbox contains 1-based terminal coordinates (x, y).
func HitTest(boxes []Hitbox, x, y int) HoverTarget {
	for _, box := range boxes {
		if box.Row == y && x >= box.StartCol && x <= box.EndCol {
			return box.Target
		}
	}
	return HoverNone
}
