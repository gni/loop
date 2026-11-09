package render

// RemapToolCalls keeps already rendered previews and replay events attached to
// their authoritative call when provider deltas arrived out of index order.
func (sr *StreamRenderer) RemapToolCalls(mapping map[int]int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	remap := func(index int) int {
		if target, ok := mapping[index]; ok {
			return target
		}
		return -1
	}
	sr.liveCallViews = remapToolMap(sr.liveCallViews, mapping)
	sr.writeResultLines = remapToolMap(sr.writeResultLines, mapping)
	sr.toolIDs = remapToolMap(sr.toolIDs, mapping)
	for id, event := range sr.toolStates {
		event.Index = remap(event.Index)
		sr.toolStates[id] = event
	}
	sr.liveActiveCall = remap(sr.liveActiveCall)
	sr.liveVisibleCall = remap(sr.liveVisibleCall)
	events := sr.liveReplayEvents[:0]
	for _, event := range sr.liveReplayEvents {
		if event.kind == "tool" {
			event.index = remap(event.index)
			if event.index < 0 {
				continue
			}
		}
		events = append(events, event)
	}
	clear(sr.liveReplayEvents[len(events):])
	sr.liveReplayEvents = events
	if sr.parser != nil {
		p := sr.parser
		p.ActiveToolIndex = remap(p.ActiveToolIndex)
		if p.ActiveToolIndex < 0 {
			p.ActiveToolName = ""
		}
		count := 0
		for _, target := range mapping {
			count = max(count, target+1)
		}
		lines := make([]int, count)
		printed, bodies := make([]bool, count), make([]bool, count)
		for i := range lines {
			lines[i] = -1
		}
		for old, target := range mapping {
			if old < len(p.ToolTitleLineNumbers) {
				lines[target] = p.ToolTitleLineNumbers[old]
			}
			if old < len(p.ToolTitlePrinted) {
				printed[target] = p.ToolTitlePrinted[old]
			}
			if old < len(p.ToolBodyStreamed) {
				bodies[target] = p.ToolBodyStreamed[old]
			}
		}
		p.ToolTitleLineNumbers, p.ToolTitlePrinted, p.ToolBodyStreamed = lines, printed, bodies
	}
}

func remapToolMap[T any](source map[int]T, mapping map[int]int) map[int]T {
	result := make(map[int]T, len(source))
	for old, value := range source {
		if target, ok := mapping[old]; ok {
			result[target] = value
		}
	}
	return result
}
