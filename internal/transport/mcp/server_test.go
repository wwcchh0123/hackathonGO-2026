package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestDisplayURLUsesPublicSessionPage(t *testing.T) {
	if got := displayURL("https://work.example/", "task_中文"); got != "https://work.example/display/?task_ref=task_%E4%B8%AD%E6%96%87" {
		t.Fatalf("display URL: %q", got)
	}
	if got := displayURL("", "ses_1"); got != "" {
		t.Fatalf("empty public URL should disable display links: %q", got)
	}
}

func TestReceiptIncludesTaskDisplayURL(t *testing.T) {
	value := withDisplayURL(session.Receipt{TaskRef: "task_existing"}, "https://work.example")
	raw, err := json.Marshal(value)
	if err != nil || !strings.Contains(string(raw), "https://work.example/display/?task_ref=task_existing") {
		t.Fatalf("display URL missing: %s (%v)", raw, err)
	}
}
