// Package progress extracts user-facing Agent messages without a second model.
package progress

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type Message struct {
	Text    string    `json:"text"`
	EventID string    `json:"event_id"`
	Time    time.Time `json:"time"`
}

// ReadMessage joins every nonempty text block, preserving the source and time.
// It never manufactures a message from a tool, error or status event.
func ReadMessage(f events.Frame) Message {
	if f.Type != "agent.message" && f.Type != "user.message" {
		return Message{}
	}
	var e struct {
		Created   time.Time                     `json:"created_at"`
		Processed time.Time                     `json:"processed_at"`
		Content   []struct{ Type, Text string } `json:"content"`
		Input     struct {
			Parts []struct{ Type, Text string } `json:"parts"`
		} `json:"input"`
	}
	if json.Unmarshal(f.Data, &e) != nil {
		return Message{}
	}
	parts := []string{}
	for _, b := range e.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	if len(parts) == 0 {
		for _, b := range e.Input.Parts {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, strings.TrimSpace(b.Text))
			}
		}
	}
	when := e.Processed
	if when.IsZero() {
		when = e.Created
	}
	return Message{Text: strings.Join(parts, "\n"), EventID: f.ID, Time: when}
}
