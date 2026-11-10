package message

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"loop/pkg/storage"
)

type ContentBlock struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Data       string `json:"data,omitempty"`
	MimeType   string `json:"mime_type,omitempty"`
	URL        string `json:"url,omitempty"`
	Signature  string `json:"signature,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

func ReadAttachment(path string) (ContentBlock, error) {
	return ReadAttachmentContext(context.Background(), path, 20<<20)
}

func ReadAttachmentContext(ctx context.Context, path string, limit int64) (ContentBlock, error) {
	data, err := storage.ReadFileContext(ctx, path, limit)
	if err != nil {
		return ContentBlock{}, err
	}
	return AttachmentFromBytes(data)
}

// AttachmentFromBytes uses the same content detection for explicit attachments
// and the file reader, without rereading or decoding an image a second time.
func AttachmentFromBytes(data []byte) (ContentBlock, error) {
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ContentBlock{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: mime}, nil
	}
	if strings.Contains(string(data), "\x00") {
		return ContentBlock{}, fmt.Errorf("unsupported binary attachment type %s", mime)
	}
	return ContentBlock{Type: "text", Text: strings.ToValidUTF8(string(data), "�")}, nil
}

func (m Message) ChatContent() any {
	if len(m.ContentBlocks) == 0 {
		return m.Content
	}
	parts := make([]map[string]any, 0, len(m.ContentBlocks)+1)
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	for _, block := range m.ContentBlocks {
		switch block.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
		case "image":
			url := block.URL
			if url == "" {
				url = "data:" + block.MimeType + ";base64," + block.Data
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
		}
	}
	return parts
}

// HasConversationContent excludes system prompts and unsent/blank messages.
// Attachments and tool calls count even when there is no plain text.
func (m Message) HasConversationContent() bool {
	return m.Role != "" && m.Role != RoleSystem && (strings.TrimSpace(m.Content) != "" || len(m.ContentBlocks) > 0 || len(m.ToolCalls) > 0)
}
