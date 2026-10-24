package devicedisplay

import (
	"encoding/json"
	"sync"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

type Event struct {
	ID   string
	Data json.RawMessage
}

type Update struct {
	Kind     string
	Session  string
	Snapshot device.Snapshot
	Event    Event
}

// Store is the small in-memory bridge between the control-service client and
// the local renderer. It keeps raw event JSON so the renderer can choose its
// own presentation without changing the upstream event contract.
type Store struct {
	mu          sync.RWMutex
	snapshot    device.Snapshot
	events      []Event
	seen        map[string]struct{}
	subscribers map[chan Update]struct{}
}

func NewStore() *Store {
	return &Store{
		seen:        make(map[string]struct{}),
		subscribers: make(map[chan Update]struct{}),
	}
}

// Snapshot returns a consistent view for the local HTTP renderer. Event data
// is copied so a browser request never observes a slice while the SSE client
// is appending a newly received frame.
func (s *Store) Snapshot() (device.Snapshot, []Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := append([]Event(nil), s.events...)
	return s.snapshot, events
}

// LastEventID is the cursor used when an SSE connection is rebuilt. The
// control service replays from that cursor, while AppendEvent still performs
// ID de-duplication to make reconnects safe when a cursor is stale.
func (s *Store) LastEventID(session string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot.SessionID != session || len(s.events) == 0 {
		return ""
	}
	return s.events[len(s.events)-1].ID
}

// Subscribe registers one local browser stream. Notifications are hints: the
// browser can reconnect and read the complete history from Snapshot if it was
// slower than the device client.
func (s *Store) Subscribe() (<-chan Update, func()) {
	ch := make(chan Update, 64)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}
}

// SetSnapshot is the session boundary. A new AgentAPI session invalidates the
// old timeline, while status changes within the same session leave its event
// history intact.
func (s *Store) SetSnapshot(snapshot device.Snapshot) {
	s.mu.Lock()
	reset := s.snapshot.SessionID != "" && snapshot.SessionID != s.snapshot.SessionID
	if reset {
		s.events = nil
		s.seen = make(map[string]struct{})
	}
	s.snapshot = snapshot
	subscribers := s.subscriberListLocked()
	s.mu.Unlock()

	kind := "snapshot"
	if reset {
		kind = "reset"
	}
	s.publish(subscribers, Update{Kind: kind, Session: snapshot.SessionID, Snapshot: snapshot})
}

// AppendEvent keeps the provider JSON untouched. Rendering concerns belong to
// the browser, so this layer only records the upstream ID and publishes a
// notification to local views.
func (s *Store) AppendEvent(event Event) bool {
	s.mu.Lock()
	if event.ID != "" {
		if _, exists := s.seen[event.ID]; exists {
			s.mu.Unlock()
			return false
		}
		s.seen[event.ID] = struct{}{}
	}
	event.Data = append(json.RawMessage(nil), event.Data...)
	s.events = append(s.events, event)
	session := s.snapshot.SessionID
	subscribers := s.subscriberListLocked()
	s.mu.Unlock()

	s.publish(subscribers, Update{Kind: "event", Session: session, Event: event})
	return true
}

func (s *Store) subscriberListLocked() []chan Update {
	result := make([]chan Update, 0, len(s.subscribers))
	for ch := range s.subscribers {
		result = append(result, ch)
	}
	return result
}

func (s *Store) publish(subscribers []chan Update, update Update) {
	for _, ch := range subscribers {
		select {
		case ch <- update:
		default:
			// The HTTP stream can always recover the complete history from the
			// store after reconnecting, so a slow browser must not block intake.
		}
	}
}
