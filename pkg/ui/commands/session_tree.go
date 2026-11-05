package commands

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/session"
	"loop/pkg/ui/style"
)

func shortEntryID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncateString(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 {
		return ""
	}
	for index := range text {
		if maxRunes == 0 {
			return text[:index] + "..."
		}
		maxRunes--
	}
	return text
}

func resolveEntryID(entries []session.Record, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("empty entry ID")
	}
	for _, e := range entries {
		if e.ID == target {
			return e.ID, nil
		}
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.ID, target) {
			matches = append(matches, e.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous entry ID prefix %q matches %d entries", target, len(matches))
	}
	return "", fmt.Errorf("entry ID %q not found", target)
}

func formatRecordTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "        "
	}
	formats := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"15:04:05",
		"15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t.Format("15:04:05")
		}
	}
	if idx := strings.IndexByte(raw, ' '); idx >= 0 && idx+1 < len(raw) {
		timePart := strings.TrimSpace(raw[idx+1:])
		if len(timePart) >= 8 && timePart[2] == ':' && timePart[5] == ':' {
			return timePart[:8]
		}
	}
	if len(raw) >= 8 && raw[2] == ':' && raw[5] == ':' {
		return raw[:8]
	}
	if len(raw) > 8 {
		return raw[:8]
	}
	return fmt.Sprintf("%-8s", raw)
}

func handleTreeCommand(
	a *agent.Agent,
	parts []string,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
) {
	if currentSessionID == nil || *currentSessionID == "" || (a != nil && a.Ephemeral) {
		fmt.Fprintln(w, "active session is ephemeral or unset; tree visualization requires a saved session.")
		return
	}

	showAll := false
	maxLimit := 0
	for i, p := range parts {
		if p == "all" || p == "--all" || p == "-a" {
			showAll = true
		} else if p == "-n" && i+1 < len(parts) {
			if n, err := strconv.Atoi(parts[i+1]); err == nil && n > 0 {
				maxLimit = n
			}
		} else if n, err := strconv.Atoi(p); err == nil && n > 0 {
			maxLimit = n
		}
	}

	entries, err := db.Entries(*currentSessionID)
	if err != nil {
		fmt.Fprintf(w, "cannot inspect session entries: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, "session history is empty.")
		return
	}

	previousID := ""
	for index := range entries {
		record := &entries[index]
		if record.Type == "session" {
			continue
		}
		if record.ID == "" {
			record.ID = fmt.Sprintf("legacy-%d", index)
			record.ParentID = previousID
			record.Type = "message"
		}
		if record.Type != "branch" {
			previousID = record.ID
		}
	}

	leafPath := session.LeafPath(entries, "")
	activeSet := make(map[string]bool, len(leafPath))
	var activeLeafID string
	if len(leafPath) > 0 {
		activeLeafID = leafPath[len(leafPath)-1].ID
		for _, r := range leafPath {
			activeSet[r.ID] = true
		}
	}

	allRecordsByID := make(map[string]session.Record, len(entries))
	for _, e := range entries {
		if e.ID != "" {
			allRecordsByID[e.ID] = e
		}
	}

	isVisible := func(e session.Record) bool {
		if e.Type == "session" {
			return false
		}
		if showAll {
			return true
		}
		if e.Type == "settings" || e.Type == "prompt" {
			return false
		}
		if e.Type == "tool" || (e.Type == "message" && e.Message.Role == "tool") {
			return false
		}
		return true
	}

	findVisibleParent := func(e session.Record) string {
		parentID := e.ParentID
		for parentID != "" {
			parentRec, ok := allRecordsByID[parentID]
			if !ok {
				break
			}
			if isVisible(parentRec) {
				return parentID
			}
			parentID = parentRec.ParentID
		}
		return ""
	}

	type dagNode struct {
		record   session.Record
		children []*dagNode
	}

	var allVisibleNodes []*dagNode
	for i := range entries {
		e := entries[i]
		if !isVisible(e) {
			continue
		}
		allVisibleNodes = append(allVisibleNodes, &dagNode{record: e})
	}

	if len(allVisibleNodes) == 0 {
		fmt.Fprintln(w, "session history contains no conversation records.")
		return
	}

	totalVisible := len(allVisibleNodes)
	collapsedCount := 0
	visibleNodes := allVisibleNodes
	if maxLimit > 0 && len(visibleNodes) > maxLimit {
		collapsedCount = len(visibleNodes) - maxLimit
		visibleNodes = visibleNodes[collapsedCount:]
	}

	nodeMap := make(map[string]*dagNode, len(visibleNodes))
	for _, node := range visibleNodes {
		nodeMap[node.record.ID] = node
	}

	var roots []*dagNode
	for _, node := range visibleNodes {
		parentID := findVisibleParent(node.record)
		if parentID == "" || nodeMap[parentID] == nil {
			roots = append(roots, node)
		} else {
			parent := nodeMap[parentID]
			parent.children = append(parent.children, node)
		}
	}

	displayLeafID := activeLeafID
	if nodeMap[displayLeafID] == nil && displayLeafID != "" {
		entryID := displayLeafID
		for entryID != "" {
			if nodeMap[entryID] != nil {
				displayLeafID = entryID
				break
			}
			if parentRec, ok := allRecordsByID[entryID]; ok {
				entryID = parentRec.ParentID
			} else {
				break
			}
		}
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	activeStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	leafStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
	dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	timeStyle := style.NewStyle().Foreground(theme.Border)

	termWidth, _ := style.GetTerminalSize()
	if termWidth <= 40 {
		termWidth = 100
	}

	fmt.Fprintln(w)
	label := "turns"
	if showAll {
		label = "records"
	}
	fmt.Fprintf(w, "%s %s (%d %s, active leaf: %s)\n",
		headerStyle.Render("Session DAG:"),
		shortEntryID(*currentSessionID),
		totalVisible,
		label,
		activeStyle.Render(shortEntryID(displayLeafID)),
	)
	if collapsedCount > 0 {
		dimNoticeStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
		fmt.Fprintf(w, "%s\n", dimNoticeStyle.Render(fmt.Sprintf("... (%d earlier %s collapsed; run /tree all or /tree %d to view full session)", collapsedCount, label, totalVisible)))
	}
	fmt.Fprintln(w)

	var renderSubtree func(node *dagNode, prefix string, showBranchMarker bool, isLastBranch bool)
	renderSubtree = func(node *dagNode, prefix string, showBranchMarker bool, isLastBranch bool) {
		isActive := activeSet[node.record.ID]
		isLeaf := node.record.ID == displayLeafID

		branchMarker := ""
		if showBranchMarker {
			if isLastBranch {
				branchMarker = "└── "
			} else {
				branchMarker = "├── "
			}
		}

		starMarker := "o "
		if isActive {
			starMarker = activeStyle.Render("* ")
		}

		typeTag := node.record.Type
		if typeTag == "" || typeTag == "message" {
			if node.record.Message.Role != "" {
				typeTag = node.record.Message.Role
			} else {
				typeTag = "msg"
			}
		}

		rawPreview := ""
		switch {
		case node.record.Type == "branch":
			rawPreview = fmt.Sprintf("switch active branch to %s", shortEntryID(node.record.ParentID))
		case node.record.Type == "compaction":
			rawPreview = "[context compaction summary]"
		case node.record.Type == "context":
			rawPreview = fmt.Sprintf("[rewritten context: %d msgs]", len(node.record.Messages))
		case len(node.record.Message.ToolCalls) > 0:
			tc := node.record.Message.ToolCalls[0]
			callSummary := fmt.Sprintf("%s(%s)", tc.Function.Name, truncateString(tc.Function.Arguments, 30))
			if len(node.record.Message.ToolCalls) > 1 {
				rawPreview = fmt.Sprintf("tool: %s (+%d)", callSummary, len(node.record.Message.ToolCalls)-1)
			} else {
				rawPreview = fmt.Sprintf("tool: %s", callSummary)
			}
		default:
			text := strings.TrimSpace(node.record.Message.Content)
			if idx := strings.IndexByte(text, '\n'); idx >= 0 {
				text = text[:idx]
			}
			rawPreview = fmt.Sprintf("%q", text)
		}

		rawTime := formatRecordTime(node.record.Timestamp)
		timeStr := timeStyle.Render(rawTime)

		leafSuffix := ""
		if isLeaf {
			leafSuffix = " " + leafStyle.Render("[ACTIVE]")
		}

		entryIDStr := dimStyle.Render(shortEntryID(node.record.ID))
		tagStr := fmt.Sprintf("[%s]", typeTag)

		baseLen := utf8.RuneCountInString(style.StripAnsi(rawTime + "  " + prefix + branchMarker + starMarker + shortEntryID(node.record.ID) + " " + tagStr + " " + leafSuffix))
		availWidth := termWidth - baseLen - 2
		if availWidth < 12 {
			availWidth = 12
		}
		preview := truncateString(rawPreview, availWidth)

		fmt.Fprintf(w, "%s  %s%s%s%s %s %s%s\n",
			timeStr,
			prefix,
			branchMarker,
			starMarker,
			entryIDStr,
			tagStr,
			preview,
			leafSuffix,
		)

		childBasePrefix := prefix
		if showBranchMarker {
			if isLastBranch {
				childBasePrefix += "    "
			} else {
				childBasePrefix += "│   "
			}
		}

		if len(node.children) == 1 {
			// Single child: stays on same branch / same column
			renderSubtree(node.children[0], childBasePrefix, false, false)
		} else if len(node.children) > 1 {
			// Multiple children: fork branches
			for idx, child := range node.children {
				isLast := (idx == len(node.children)-1)
				renderSubtree(child, childBasePrefix, true, isLast)
			}
		}
	}

	for idx, root := range roots {
		isLast := (idx == len(roots)-1)
		showBranch := len(roots) > 1
		renderSubtree(root, "", showBranch, isLast)
	}

	fmt.Fprintln(w)
	if showAll {
		fmt.Fprintln(w, dimStyle.Render("tip: run /branch <entry_id> to switch active branch."))
	} else {
		fmt.Fprintln(w, dimStyle.Render("tip: run /branch <entry_id> to switch active branch (or /tree all to see raw tool records)."))
	}
	fmt.Fprintln(w)
}
