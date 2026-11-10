package interceptor

import (
	"context"
	"fmt"
	"io"

	"loop/pkg/agent"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

type ApprovalByteReader struct {
	Context context.Context
	Input   <-chan byte
}

func (r *ApprovalByteReader) GetInputContext() context.Context { return r.Context }
func (r *ApprovalByteReader) GetInputChan() <-chan byte        { return r.Input }

func (r *ApprovalByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.Context != nil && r.Context.Err() != nil {
		return 0, r.Context.Err()
	}

	var done <-chan struct{}
	if r.Context != nil {
		done = r.Context.Done()
	}
	var first byte
	var ok bool
	select {
	case <-done:
		return 0, r.Context.Err()
	case first, ok = <-r.Input:
	}
	if !ok {
		return 0, io.EOF
	}
	p[0] = first
	n := 1
	for n < len(p) {
		select {
		case next, ok := <-r.Input:
			if !ok {
				return n, nil
			}
			p[n] = next
			n++
		default:
			return n, nil
		}
	}
	return n, nil
}

func (ki *KeyInterceptorReader) BeginApprovalInput() io.Reader {
	if ki.approvalCancel != nil {
		ki.approvalCancel()
		ki.approvalCancel = nil
	}
	if ki.ApprovalChan == nil {
		ki.ApprovalChan = make(chan byte, 1000)
	}
	ki.DrainApprovalInput()
	if ki.ApprovalReader == nil {
		ki.ApprovalReader = &ApprovalByteReader{Input: ki.ApprovalChan, Context: ki.InputContext}
	}
	ctx := ki.InputContext
	if ctx == nil {
		ctx = context.Background()
	}
	if ki.Agent != nil && ki.ActiveTurn.Load() {
		root := ctx
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ki.Agent.Context())
		stop := context.AfterFunc(root, cancel)
		if root.Err() != nil {
			cancel()
		}
		ki.approvalCancel = func() { stop(); cancel() }
	}
	ki.ApprovalReader.Context = ctx
	return ki.ApprovalReader
}

func (ki *KeyInterceptorReader) EndApprovalInput() {
	if ki.approvalCancel != nil {
		ki.approvalCancel()
		ki.approvalCancel = nil
	}
	ki.DrainApprovalInput()
}

func (ki *KeyInterceptorReader) DrainApprovalInput() {
	if ki.ApprovalChan == nil {
		return
	}
	for {
		select {
		case _, ok := <-ki.ApprovalChan:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (ki *KeyInterceptorReader) PrintCancelMessage() {
	output := ki.W
	if ki.Agent != nil {
		if wh, ok := ki.Agent.UI.(interface{ GetPPWriter() io.Writer }); ok {
			if w := wh.GetPPWriter(); w != nil {
				output = w
			}
		} else if w := ki.Agent.CurrentWriter(); w != nil {
			output = w
		}
	}
	if output != nil {
		fmt.Fprintln(output, "\n[operation cancelled by user]")
	}
}

func (ki *KeyInterceptorReader) HandleSubagentCancellation(rawInput <-chan byte, cancelParent context.CancelFunc, agentName string) {
	output := ki.W
	if ki.Agent != nil && ki.Agent.CurrentWriter() != nil {
		output = ki.Agent.CurrentWriter()
	}
	var theme style.UITheme
	if ki.Agent != nil && ki.Agent.Config != nil {
		theme = style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
	}
	decision := interactive.AskForSubagentCancellationWithReader(
		output,
		&ApprovalByteReader{Input: rawInput, Context: ki.InputContext},
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
