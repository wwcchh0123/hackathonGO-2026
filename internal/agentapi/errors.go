package agentapi

import (
	"errors"
	"fmt"
)

// Error reports safe metadata, never provider response bodies or credentials.
type Error struct {
	Operation  string
	StatusCode int
	Ambiguous  bool // A write may have reached the server. Never blindly repeat it.
	Cause      error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("AgentAPI %s: HTTP %d", e.Operation, e.StatusCode)
	}
	return "AgentAPI " + e.Operation + ": request or response failed"
}
func (e *Error) Unwrap() error { return e.Cause }
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}
func Uncertain(err error) bool { var e *Error; return errors.As(err, &e) && e.Ambiguous }
func Retryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.StatusCode == 0 || e.StatusCode == 408 || e.StatusCode == 429 || e.StatusCode >= 500)
}
func KnownStatus(s string) bool {
	return s == "idle" || s == "running" || s == "rescheduling" || s == "terminated"
}
