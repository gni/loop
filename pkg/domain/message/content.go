package message

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type ContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Data      string `json:"data,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	URL       string `json:"url,omitempty"`
	Signature string `json:"signature,omitempty"`
 ToolCallID string `json:"tool_call_id,omitempty"`
}

func ReadAttachment(path string) (ContentBlock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ContentBlock{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 20<<20 {
		return ContentBlock{}, fmt.Errorf("attachment must be a regular file at most 20 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return ContentBlock{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
	if err != nil {
		return ContentBlock{}, err
	}
	if len(data) > 20<<20 {
		return ContentBlock{}, fmt.Errorf("attachment exceeds 20 MiB")
	}
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
