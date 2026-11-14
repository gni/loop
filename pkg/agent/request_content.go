package agent

import (
	"strings"

	"loop/pkg/db"
	"loop/pkg/domain/message"
)

// Tool results use one text body across providers. Normalize it before budgeting
// without changing saved messages or the order of user attachment blocks.
func mergeRequestTextBlocks(msg db.Message) db.Message {
	if msg.Role != "tool" {
		return msg
	}
	hasText := false
	for _, block := range msg.ContentBlocks {
		if block.Type == "text" {
			hasText = true
			break
		}
	}
	if !hasText {
		return msg
	}
	var text strings.Builder
	text.WriteString(msg.Content)
	blocks := make([]message.ContentBlock, 0, len(msg.ContentBlocks))
	for _, block := range msg.ContentBlocks {
		if block.Type != "text" {
			blocks = append(blocks, block)
			continue
		}
		if block.Text == "" || strings.Contains(msg.Content, block.Text) {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(block.Text)
	}
	msg.Content, msg.ContentBlocks = text.String(), blocks
	return msg
}

func responsesBlockParts(blocks []message.ContentBlock, textKind string) []map[string]any {
	var parts []map[string]any
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, map[string]any{"type": textKind, "text": block.Text})
			continue
		}
		if block.Type != "image" {
			continue
		}
		image := block.URL
		if image == "" {
			image = "data:" + block.MimeType + ";base64," + block.Data
		}
		parts = append(parts, map[string]any{"type": "input_image", "image_url": image})
	}
	return parts
}

func generateContentBlockParts(blocks []message.ContentBlock) []map[string]any {
	var parts []map[string]any
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, map[string]any{"text": block.Text})
			continue
		}
		if block.Type != "image" {
			continue
		}
		if block.URL != "" {
			parts = append(parts, map[string]any{"fileData": map[string]string{"mimeType": block.MimeType, "fileUri": block.URL}})
		} else {
			parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": block.MimeType, "data": block.Data}})
		}
	}
	return parts
}
