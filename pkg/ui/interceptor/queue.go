package interceptor

import (
	"fmt"
	"strings"
)

type PromptInput struct {
	Text   string
	Pastes []string
}

func (ki *KeyInterceptorReader) EnqueuePrompt(prompt string, pastes []string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return 0
	}
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	ki.PromptQueue = append(ki.PromptQueue, PromptInput{prompt, pastes})
	return len(ki.PromptQueue)
}

func (ki *KeyInterceptorReader) DequeuePrompt() (PromptInput, bool) {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return PromptInput{}, false
	}
	prompt := ki.PromptQueue[0]
	ki.PromptQueue[0] = PromptInput{}
	ki.PromptQueue = ki.PromptQueue[1:]
	return prompt, true
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
	return true
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
	if ki.OnQueueChanged != nil {
		ki.OnQueueChanged()
	}
	return notice, true
}

func (ki *KeyInterceptorReader) GetQueuedPrompts() []PromptInput {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return nil
	}
	copied := make([]PromptInput, len(ki.PromptQueue))
	copy(copied, ki.PromptQueue)
	return copied
}

func (ki *KeyInterceptorReader) QueueLen() int {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	return len(ki.PromptQueue)
}
