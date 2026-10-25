package session

import (
	"encoding/json"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
	"time"
)

// Command targets are frozen at admission, independently of device selection.
type Command struct {
	Action         string `json:"action"`
	Prompt         string `json:"prompt,omitempty"`
	TaskRef        string `json:"task_ref,omitempty"`
	ParentRef      string `json:"parent_task_ref,omitempty"`
	ID             string `json:"-"`
	Source         string `json:"-"`
	ConversationID string `json:"-"`
}
type Receipt struct {
	Accepted  bool   `json:"accepted"`
	Message   string `json:"message"`
	TaskRef   string `json:"task_ref"`
	CommandID string `json:"command_id"`
}
type CommandStatus struct {
	ID         string    `json:"command_id"`
	State      string    `json:"state"`
	Phase      string    `json:"phase"`
	EventID    string    `json:"input_event_id,omitempty"`
	Error      string    `json:"error,omitempty"`
	AcceptedAt time.Time `json:"accepted_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Message = progress.Message

type Task struct {
	Ref                string    `json:"task_ref"`
	SessionID          string    `json:"session_id,omitempty"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	Runtime            string    `json:"runtime,omitempty"`
	Archived           bool      `json:"archived"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Summary            *Message  `json:"summary"`
	SummaryUnavailable bool      `json:"summary_unavailable,omitempty"`
	CanContinue        bool      `json:"can_continue"`
	ContinueReason     string    `json:"continue_reason,omitempty"`
}
type ListOptions struct {
	Limit           int    `json:"limit,omitempty"`
	Page            string `json:"page,omitempty"`
	Status          string `json:"status,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	CreatedAfter    string `json:"created_after,omitempty"`
	CreatedBefore   string `json:"created_before,omitempty"`
	SkipSummary     bool   `json:"-"`
}
type TaskPage struct {
	Data      []Task      `json:"data"`
	NextPage  string      `json:"next_page,omitempty"`
	HasMore   bool        `json:"has_more"`
	Sort      string      `json:"sort"`
	Scope     string      `json:"scope"`
	QueriedAt time.Time   `json:"queried_at"`
	Filters   ListOptions `json:"filters"`
	Partial   bool        `json:"partial"`
}
type DetailOptions struct {
	EventsLimit *int   `json:"events_limit,omitempty"`
	EventsPage  string `json:"events_page,omitempty"`
	FilesPage   string `json:"files_page,omitempty"`
	CommandID   string `json:"command_id,omitempty"`
}
type Detail struct {
	Task
	QueriedAt        time.Time         `json:"queried_at"`
	StopReason       string            `json:"stop_reason"`
	Messages         []Message         `json:"messages"`
	Events           []json.RawMessage `json:"events"`
	EventsNextPage   string            `json:"events_next_page,omitempty"`
	EventsHasMore    bool              `json:"events_has_more"`
	Files            agentapi.FilePage `json:"files"`
	FilesUnavailable bool              `json:"files_unavailable,omitempty"`
	Command          *CommandStatus    `json:"command,omitempty"`
	ResultURL        string            `json:"result_url,omitempty"`
}
