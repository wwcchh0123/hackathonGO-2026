package agentapi

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorHelpersClassifySafeProviderFailures(t *testing.T) {
	root := errors.New("upstream detail")
	err := &Error{Operation: "POST message", StatusCode: 429, Ambiguous: true, Cause: root}
	if got := err.Error(); got != "AgentAPI POST message: HTTP 429" {
		t.Fatalf("error text: %q", got)
	}
	if !errors.Is(err, root) || !IsStatus(err, 429) || !Uncertain(err) || !Retryable(err) {
		t.Fatalf("classification lost: %v", err)
	}
	if Retryable(&Error{StatusCode: 400}) || Uncertain(&Error{StatusCode: 400}) {
		t.Fatal("client errors must not be retryable or ambiguous")
	}
	if !Retryable(&Error{StatusCode: 0}) || !Retryable(&Error{StatusCode: 408}) || !Retryable(&Error{StatusCode: 500}) {
		t.Fatal("transport, timeout, and server errors should be retryable")
	}
	if got := (&Error{Operation: "GET session"}).Error(); got != "AgentAPI GET session: request or response failed" {
		t.Fatalf("unknown error text: %q", got)
	}
	for _, status := range []string{"idle", "running", "rescheduling", "terminated"} {
		if !KnownStatus(status) {
			t.Fatalf("known status rejected: %s", status)
		}
	}
	for _, status := range []string{"", "created", "failed"} {
		if KnownStatus(status) {
			t.Fatalf("unknown status accepted: %s", status)
		}
	}
	if IsStatus(fmt.Errorf("other"), 500) || Uncertain(fmt.Errorf("other")) {
		t.Fatal("non-provider errors were classified")
	}
}
