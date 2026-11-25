package interceptor

import (
	"fmt"
	"maps"
	"strings"
)

type PromptInput struct {
	Text       string
	Pastes     []string
	CodeBlocks map[string]string
}

func clonePromptInput(in PromptInput) PromptInput {
	out := PromptInput{Text: in.Text}
	if len(in.Pastes) > 0 {
		out.Pastes = append([]string(nil), in.Pastes...)
	}
	if len(in.CodeBlocks) > 0 {
		out.CodeBlocks = make(map[string]string, len(in.CodeBlocks))
		maps.Copy(out.CodeBlocks, in.CodeBlocks)
	}
	return out
}

// ExpandPromptCodeBlocks replaces collapsed paste tags with their stored payloads.
func ExpandPromptCodeBlocks(line string, blocks map[string]string) string {
	if len(blocks) == 0 {
		return line
	}
	var replacements []string
	for tag, code := range blocks {
		if strings.Contains(line, tag) {
			replacements = append(replacements, tag, code)
		}
	}
	if len(replacements) == 0 {
		return line
	}
	return strings.NewReplacer(replacements...).Replace(line)
}

func (ki *KeyInterceptorReader) EnqueuePrompt(prompt string, pastes []string) int {
	return ki.EnqueuePromptInput(PromptInput{Text: prompt, Pastes: pastes})
}

func (ki *KeyInterceptorReader) EnqueuePromptInput(input PromptInput) int {
	input.Text = strings.TrimSpace(input.Text)
	if input.Text == "" {
		return 0
	}
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	ki.PromptQueue = append(ki.PromptQueue, clonePromptInput(input))
	return len(ki.PromptQueue)
}

func (ki *KeyInterceptorReader) DequeuePrompt() (PromptInput, bool) {
	return ki.DequeuePromptIf(nil)
}

// DequeuePromptIf atomically inspects the head of the queue and removes it only
// when match is nil or returns true.
func (ki *KeyInterceptorReader) DequeuePromptIf(match func(PromptInput) bool) (PromptInput, bool) {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return PromptInput{}, false
	}
	head := ki.PromptQueue[0]
	if match != nil && !match(clonePromptInput(head)) {
		return PromptInput{}, false
	}
	ki.PromptQueue[0] = PromptInput{}
	ki.PromptQueue = ki.PromptQueue[1:]
	if len(ki.PromptQueue) == 0 {
		ki.PromptQueue = nil
	}
	return head, true
}

func (ki *KeyInterceptorReader) HasQueuedPrompts() bool {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	return len(ki.PromptQueue) > 0
}

func (ki *KeyInterceptorReader) ClearQueue() int {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	n := len(ki.PromptQueue)
	ki.PromptQueue = nil
	return n
}

// UndoQueuedPrompt removes the newest waiting prompt. The running turn and
// current draft are owned separately and remain untouched.
func (ki *KeyInterceptorReader) UndoQueuedPrompt() bool {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return false
	}
	last := len(ki.PromptQueue) - 1
	ki.PromptQueue[last] = PromptInput{}
	ki.PromptQueue = ki.PromptQueue[:last]
	if len(ki.PromptQueue) == 0 {
		ki.PromptQueue = nil
	}
	return true
}

// NotifyQueueChanged notifies UI listeners after queue membership changes.
func (ki *KeyInterceptorReader) NotifyQueueChanged() {
	if ki != nil && ki.OnQueueChanged != nil {
		ki.OnQueueChanged()
	}
}

// ControlQueue is shared by local commands and shortcuts. Notifications run
// after releasing the queue lock, so rendering cannot block queue consumers.
func (ki *KeyInterceptorReader) ControlQueue(action string) (string, bool) {
	var notice string
	switch strings.ToLower(action) {
	case "undo":
		if ki.UndoQueuedPrompt() {
			notice = "removed the newest queued prompt."
		} else {
			notice = "prompt queue is empty."
		}
	case "clear":
		notice = fmt.Sprintf("cleared %d queued prompt(s).", ki.ClearQueue())
	default:
		return "", false
	}
	ki.NotifyQueueChanged()
	return notice, true
}

func (ki *KeyInterceptorReader) GetQueuedPrompts() []PromptInput {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return nil
	}
	copied := make([]PromptInput, len(ki.PromptQueue))
	for i, item := range ki.PromptQueue {
		copied[i] = clonePromptInput(item)
	}
	return copied
}

func (ki *KeyInterceptorReader) QueueLen() int {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	return len(ki.PromptQueue)
}
