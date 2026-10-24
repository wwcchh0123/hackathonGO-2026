// Package agentapi isolates the cloud execution contract from orchestration.
package agentapi

import (
	"context"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type Session struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type File struct {
	Path string
	Data []byte
}

// Message keeps user text unchanged while correlating acceptance with the stream.
type Message struct {
	Text      string
	CommandID string
}

type AcceptedInput struct{ EventID string }

type AcceptedProvider interface {
	SendMessage(context.Context, string, Message) (AcceptedInput, error)
}

type Provider interface {
	Create(context.Context) (Session, error)
	Send(context.Context, string, string) error
	Interrupt(context.Context, string) error
	Status(context.Context, string) (Session, error)
	Stream(context.Context, string, func(events.Frame) error) error
	Artifacts(context.Context, string) ([]File, error)
}
