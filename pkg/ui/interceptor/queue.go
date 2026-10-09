package interceptor

import (
	"strings"
)

func (ki *KeyInterceptorReader) EnqueuePrompt(prompt string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return 0
	}
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	ki.PromptQueue = append(ki.PromptQueue, prompt)
	return len(ki.PromptQueue)
}

func (ki *KeyInterceptorReader) DequeuePrompt() (string, bool) {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return "", false
	}
	prompt := ki.PromptQueue[0]
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

func (ki *KeyInterceptorReader) GetQueuedPrompts() []string {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	if len(ki.PromptQueue) == 0 {
		return nil
	}
	copied := make([]string, len(ki.PromptQueue))
	copy(copied, ki.PromptQueue)
	return copied
}

func (ki *KeyInterceptorReader) QueueLen() int {
	ki.PromptQueueMu.Lock()
	defer ki.PromptQueueMu.Unlock()
	return len(ki.PromptQueue)
}
