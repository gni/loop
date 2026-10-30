package message

func Clone(input Message) Message {
	output := input
	if input.Usage != nil {
		usage := *input.Usage
		if usage.Cost != nil {
			cost := *usage.Cost
			usage.Cost = &cost
		}
		output.Usage = &usage
	}
	output.Structured = append(input.Structured[:0:0], input.Structured...)
	output.ContentBlocks = append(input.ContentBlocks[:0:0], input.ContentBlocks...)
	output.CacheControl = append(input.CacheControl[:0:0], input.CacheControl...)
	output.ToolCalls = append(input.ToolCalls[:0:0], input.ToolCalls...)
	for i := range output.ToolCalls {
		if input.ToolCalls[i].Index != nil {
			index := *input.ToolCalls[i].Index
			output.ToolCalls[i].Index = &index
		}
	}
	if input.Cost != nil {
		cost := *input.Cost
		output.Cost = &cost
	}
	return output
}

func CloneAll(input []Message) []Message {
	output := make([]Message, len(input))
	for i, entry := range input {
		output[i] = Clone(entry)
	}
	return output
}
