package llm

import (
	"context"
	"sync"

	"oa-agent/backend/internal/harness"
)

// Switch is a harness.Provider whose target can be replaced at runtime, so a saved configuration takes
// effect immediately for the next model call without restarting the agent.
type Switch struct {
	mu sync.RWMutex
	p  harness.Provider
}

func NewSwitch(p harness.Provider) *Switch { return &Switch{p: p} }

func (s *Switch) Set(p harness.Provider) { s.mu.Lock(); s.p = p; s.mu.Unlock() }

func (s *Switch) Chat(ctx context.Context, m []harness.Message, t []harness.ToolSpec) (harness.Response, error) {
	s.mu.RLock()
	p := s.p
	s.mu.RUnlock()
	return p.Chat(ctx, m, t)
}
