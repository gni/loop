package agent

import (
	"strings"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/ui/style"
)

// CompressionSummaryPreview limits the terminal output without changing the
// summary stored in session history or sent to the model.
func CompressionSummaryPreview(summary string) string {
	summary = style.StripAnsi(strings.TrimSpace(summary))
	summary = strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\t') || r == 127 {
			return -1
		}
		return r
	}, summary)
	const maxPreviewBytes = 4096
	if len(summary) <= maxPreviewBytes {
		return summary
	}
	return filetool.UTF8SafePrefix(summary, maxPreviewBytes) +
		"\n[Preview shortened. The complete summary remains in session history.]"
}
