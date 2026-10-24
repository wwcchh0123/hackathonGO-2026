// Package device publishes replaceable device snapshots, independent of transport.
package device

import "sync"

type Snapshot struct {
	DeviceID  string `json:"device_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	Prompt    string `json:"prompt"`
	ResultURL string `json:"result_url"`
	Error     string `json:"error,omitempty"`
	Revision  uint64 `json:"revision"`
}
type State struct {
	mu      sync.Mutex
	value   Snapshot
	changed chan struct{}
}

func NewState(id string) *State {
	return &State{value: Snapshot{DeviceID: id, Status: "ready", Summary: "准备好了，说说你想做什么"}, changed: make(chan struct{})}
}
func (s *State) Watch() (Snapshot, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.changed
}
func (s *State) Get() Snapshot { v, _ := s.Watch(); return v }
func (s *State) Update(fn func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.value)
	s.value.Revision++
	close(s.changed)
	s.changed = make(chan struct{})
}
