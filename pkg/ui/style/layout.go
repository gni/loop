package style

// ActivityGapLines is the single spacing policy for terminal output.
// It controls the empty rows above the pinned stats bar and between agent activities.
const ActivityGapLines = 1

const scrollAreaControlRows = 5

// ConsoleScrollBottom is the last row reserved for scrollable output.
func ConsoleScrollBottom(height, pasteRows int) int {
	bottom := height - scrollAreaControlRows - ActivityGapLines - pasteRows
	if bottom < 1 {
		return 1
	}
	return bottom
}

// ConsoleScrollRegionOffset keeps the status bar's scroll region in sync.
func ConsoleScrollRegionOffset() int {
	return scrollAreaControlRows + ActivityGapLines - 2
}
