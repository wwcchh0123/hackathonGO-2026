package session

import (
	"errors"
	"fmt"
)

// Error is the stable business error contract shared by HTTP and MCP. The
// message remains human readable, while callers should branch on Code.
type Error struct {
	Code      string `json:"code"`
	Current   any    `json:"current,omitempty"`
	Requested any    `json:"requested,omitempty"`
	Next      string `json:"next,omitempty"`
	Cause     error  `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Cause.Error()
	}
	if e.Next != "" {
		return fmt.Sprintf("%s (next: %s)", e.Code, e.Next)
	}
	return e.Code
}
func (e *Error) Unwrap() error { return e.Cause }

func business(code string, current, requested any, next string) error {
	return &Error{Code: code, Current: current, Requested: requested, Next: next}
}

// ErrorResponse is the JSON business-error envelope on both transports.
func ErrorResponse(err error) map[string]any {
	value := map[string]any{"code": "request_failed", "message": err.Error(), "current": "", "requested": "", "next": "correct the request and retry"}
	if errors.Is(err, ErrCursorExpired) {
		value["code"] = "cursor_invalid"
		value["next"] = "list the first page again"
	}
	if errors.Is(err, ErrReference) {
		value["code"] = "task_reference_expired"
		value["next"] = "list tasks again"
	}
	var typed *Error
	if errors.As(err, &typed) {
		value["code"], value["current"], value["requested"], value["next"] = typed.Code, typed.Current, typed.Requested, typed.Next
	}
	return value
}
