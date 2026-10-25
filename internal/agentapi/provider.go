// Package agentapi isolates the cloud execution contract from orchestration.
package agentapi

import (
	"context"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type Session struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Title         string            `json:"title"`
	Runtime       string            `json:"runtime"`
	AgentID       string            `json:"agent_id,omitempty"`
	AgentVersion  int               `json:"agent_version,omitempty"`
	EnvironmentID string            `json:"environment_id,omitempty"`
	Model         Model             `json:"model,omitempty"`
	ArchivedAt    *time.Time        `json:"archived_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Metadata      map[string]string `json:"-"`
}

type Model struct {
	ID     string `json:"id,omitempty"`
	Effort string `json:"effort,omitempty"`
}
type NewSession struct {
	Title    string
	Metadata map[string]string
	Model    Model
}
type SessionQuery struct {
	Limit                       int
	Page, AgentID, Status       string
	IncludeArchived             bool
	CreatedAfter, CreatedBefore string
}
type SessionPage struct {
	Data     []Session
	NextPage string
}
type EventQuery struct {
	Limit       int
	Page, Order string
	Types       []string
}
type EventPage struct {
	Data     []events.Frame
	NextPage string
}
type FileInfo struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Size      int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}
type FilePage struct {
	Data     []FileInfo `json:"data"`
	NextPage string     `json:"next_page,omitempty"`
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

type Provider interface {
	Create(context.Context, ...NewSession) (Session, error)
	Interrupt(context.Context, string) error
	Status(context.Context, string) (Session, error)
	Stream(context.Context, string, func(events.Frame) error) error
	Artifacts(context.Context, string) ([]File, error)
	SendMessage(context.Context, string, Message) (AcceptedInput, error)
	ListSessions(context.Context, SessionQuery) (SessionPage, error)
	ListEvents(context.Context, string, EventQuery) (EventPage, error)
	ListFiles(context.Context, string, int, string) (FilePage, error)
}
