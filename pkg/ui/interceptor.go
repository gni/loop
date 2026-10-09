package ui

import (
	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui/interceptor"
)

type KeyInterceptorReader = interceptor.KeyInterceptorReader
type keyInterceptorReader = interceptor.KeyInterceptorReader
type CRNLWriter = interceptor.CRNLWriter
type crnlWriter = interceptor.CRNLWriter

func calculateActiveTokenUsage(a *agent.Agent, messages []db.Message, allowedTools []string, mam *swarm.MultiAgentManager) (int, int, bool) {
	return interceptor.CalculateActiveTokenUsage(a, messages, allowedTools, mam)
}

func activeToolAllowlist(reader *KeyInterceptorReader) []string {
	return interceptor.ActiveToolAllowlist(reader)
}
