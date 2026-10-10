package interceptor

import (
	"context"
	"fmt"
	"io"
	"time"

	"loop/pkg/agent"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

type ApprovalByteReader struct {
	Input <-chan byte
}

func (r *ApprovalByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	first, ok := <-r.Input
	if !ok {
		return 0, io.EOF
	}
	p[0] = first
	if first != 27 || len(p) == 1 {
		return 1, nil
	}

	timer := time.NewTimer(15 * time.Millisecond)
	defer timer.Stop()

	n := 1
	for n < len(p) && n < 3 {
		select {
		case next, channelOpen := <-r.Input:
			if !channelOpen {
				return n, nil
			}
			p[n] = next
			n++
		case <-timer.C:
			return n, nil
		}
	}
	return n, nil
}

func (ki *KeyInterceptorReader) BeginApprovalInput() io.Reader {
	if ki.ApprovalChan == nil {
		ki.ApprovalChan = make(chan byte, 1000)
	}
	ki.DrainApprovalInput()
	if ki.ApprovalReader == nil {
		ki.ApprovalReader = &ApprovalByteReader{Input: ki.ApprovalChan}
	}
	return ki.ApprovalReader
}

func (ki *KeyInterceptorReader) EndApprovalInput() {
	ki.DrainApprovalInput()
}

func (ki *KeyInterceptorReader) DrainApprovalInput() {
	ki.drainApprovalInput()
}

func (ki *KeyInterceptorReader) drainApprovalInput() {
	if ki.ApprovalChan == nil {
		return
	}
	for {
		select {
		case _, channelOpen := <-ki.ApprovalChan:
			if !channelOpen {
				return
			}
		default:
			return
		}
	}
}

func (ki *KeyInterceptorReader) PrintCancelMessage() {
	ki.printCancelMessage()
}

func (ki *KeyInterceptorReader) printCancelMessage() {
	output := ki.W
	if ki.Agent != nil {
		if wh, ok := ki.Agent.UI.(interface{ GetPPWriter() io.Writer }); ok {
			if w := wh.GetPPWriter(); w != nil {
				output = w
			}
		} else if ki.Agent.CurrentWriter != nil {
			output = ki.Agent.CurrentWriter
		}
	}
	if output != nil {
		fmt.Fprintln(output, "\n[operation cancelled by user]")
	}
}

func (ki *KeyInterceptorReader) HandleSubagentCancellation(rawInput <-chan byte, cancelParent context.CancelFunc, agentName string) {
	ki.handleSubagentCancellation(rawInput, cancelParent, agentName)
}

func (ki *KeyInterceptorReader) handleSubagentCancellation(rawInput <-chan byte, cancelParent context.CancelFunc, agentName string) {
	output := ki.W
	if ki.Agent != nil && ki.Agent.CurrentWriter != nil {
		output = ki.Agent.CurrentWriter
	}
	var theme style.UITheme
	if ki.Agent != nil && ki.Agent.Config != nil {
		theme = style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
	}
	decision := interactive.AskForSubagentCancellationWithReader(
		output,
		&ApprovalByteReader{Input: rawInput},
		theme,
		agentName,
	)

	switch decision {
	case agent.SubagentCancellationContinue:
		fmt.Fprintln(output, "\n[subagent cancellation dismissed]")
	case agent.SubagentCancellationSkipCurrent:
		if ki.MAM != nil && ki.MAM.CancelSubagentTurn(agentName) {
			fmt.Fprintf(output, "\n[skipped subagent: %s]\n", agentName)
		} else {
			fmt.Fprintf(output, "\n[subagent already finished: %s]\n", agentName)
		}
	case agent.SubagentCancellationStopAll:
		if ki.MAM != nil {
			ki.MAM.CancelAllActiveSubagents()
		}
		if cancelParent != nil {
			cancelParent()
		}
		if ki.OnClearCancelFunc != nil {
			ki.OnClearCancelFunc()
		}
		fmt.Fprintln(output, "\n\n[operation cancelled by user]")
	}

	ki.ResetTypeAheadLocked()
}
