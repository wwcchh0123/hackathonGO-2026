package agentapi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

func waitMockIdle(t *testing.T, mock *Mock, id string) Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, err := mock.Status(context.Background(), id)
		if err == nil && session.Status == "idle" && session.UpdatedAt.After(session.CreatedAt) {
			return session
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("mock session did not become idle")
	return Session{}
}

func TestMockLifecycleFiltersEventsAndPublishesArtifacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock := NewMock(ctx)
	mock.Step = 0
	created, err := mock.Create(ctx, NewSession{Title: "demo", Metadata: map[string]string{"device_id": "a"}})
	if err != nil || created.ID == "" || created.Title != "demo" || created.Metadata["device_id"] != "a" {
		t.Fatalf("create: %+v %v", created, err)
	}
	if len(ID()) != 32 {
		t.Fatal("ID should contain 16 random bytes")
	}
	accepted, err := mock.SendMessage(ctx, created.ID, Message{Text: "做一个红色 <番茄钟>", CommandID: "cmd-1"})
	if err != nil || accepted.EventID == "" {
		t.Fatalf("send: %+v %v", accepted, err)
	}
	waitMockIdle(t, mock, created.ID)

	files, err := mock.Artifacts(ctx, created.ID)
	if err != nil || len(files) != 1 || !strings.Contains(string(files[0].Data), "#e64d5c") || !strings.Contains(string(files[0].Data), "&lt;番茄钟&gt;") {
		t.Fatalf("artifacts: %v %+v", err, files)
	}
	page, err := mock.ListFiles(ctx, created.ID, 10, "")
	if err != nil || len(page.Data) != 1 || page.Data[0].Path != "index.html" || page.Data[0].Size == 0 {
		t.Fatalf("files: %v %+v", err, page)
	}
	empty, err := mock.ListFiles(ctx, created.ID, 10, "older")
	if err != nil || len(empty.Data) != 0 {
		t.Fatalf("file page should be empty in mock: %v %+v", err, empty)
	}

	all, err := mock.ListEvents(ctx, created.ID, EventQuery{Limit: 100})
	if err != nil || len(all.Data) < 4 || all.Data[0].Type != "session.created" {
		t.Fatalf("events: %v %+v", err, all)
	}
	desc, err := mock.ListEvents(ctx, created.ID, EventQuery{Limit: 2, Order: "desc", Types: []string{"session.status_idle"}})
	if err != nil || len(desc.Data) != 1 || desc.Data[0].Type != "session.status_idle" {
		t.Fatalf("filtered events: %v %+v", err, desc)
	}
	if _, err := mock.ListEvents(ctx, created.ID, EventQuery{Page: "missing"}); err != nil {
		t.Fatalf("unknown event cursor should be an empty page: %v", err)
	}
	var streamed int
	err = mock.Stream(ctx, created.ID, func(events.Frame) error {
		streamed++
		if streamed == len(all.Data) {
			return io.EOF
		}
		return nil
	})
	if err != io.EOF || streamed != len(all.Data) {
		t.Fatalf("stream: count=%d want=%d err=%v", streamed, len(all.Data), err)
	}
	if err := mock.Stream(ctx, created.ID, func(events.Frame) error { return errors.New("stop") }); err == nil {
		t.Fatal("stream callback error was swallowed")
	}

	second, err := mock.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list, err := mock.ListSessions(ctx, SessionQuery{Limit: 1})
	if err != nil || len(list.Data) != 1 || list.NextPage == "" {
		t.Fatalf("session pagination: %v %+v", err, list)
	}
	if _, err := mock.ListSessions(ctx, SessionQuery{Page: "missing"}); err == nil {
		t.Fatal("unknown session cursor accepted")
	}
	if filtered, err := mock.ListSessions(ctx, SessionQuery{Status: "running"}); err != nil || len(filtered.Data) != 0 {
		t.Fatalf("status filter: %v %+v", err, filtered)
	}
	if _, err := mock.Status(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mock.SendMessage(ctx, "missing", Message{Text: "x"}); err == nil {
		t.Fatal("unknown session accepted a message")
	}
}

func TestMockInterruptAndContextFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock := NewMock(ctx)
	mock.Step = time.Hour
	session, err := mock.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mock.SendMessage(ctx, session.ID, Message{Text: "long"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mock.SendMessage(ctx, session.ID, Message{Text: "duplicate"}); err == nil {
		t.Fatal("running session accepted a second message")
	}
	if err := mock.Interrupt(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := mock.Status(ctx, session.ID); err != nil || got.Status != "idle" {
		t.Fatalf("interrupted status: %+v %v", got, err)
	}
	if err := mock.Interrupt(context.Background(), "missing"); err == nil {
		t.Fatal("unknown session interrupt accepted")
	}
	canceled, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if _, err := mock.Create(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create: %v", err)
	}
	if _, err := mock.Status(canceled, session.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled status: %v", err)
	}
	if _, err := mock.SendMessage(canceled, session.ID, Message{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled send: %v", err)
	}
	if _, err := mock.Artifacts(canceled, session.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled artifacts: %v", err)
	}
}

func TestMockCatalogPaginationTimeAndArchiveFilters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock := NewMock(ctx)
	first, _ := mock.Create(ctx)
	second, _ := mock.Create(ctx)
	third, _ := mock.Create(ctx)
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	mock.mu.Lock()
	mock.sessions[first.ID].info.CreatedAt = old
	mock.sessions[first.ID].info.ArchivedAt = &now
	mock.sessions[second.ID].info.CreatedAt = now
	mock.sessions[third.ID].info.CreatedAt = now
	mock.mu.Unlock()
	page, err := mock.ListSessions(ctx, SessionQuery{Limit: 1, IncludeArchived: true})
	if err != nil || page.NextPage == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	next, err := mock.ListSessions(ctx, SessionQuery{Limit: 2, Page: page.NextPage, IncludeArchived: true})
	if err != nil || len(next.Data) != 2 || next.Data[1].ID != first.ID {
		t.Fatalf("next page: %+v %v", next, err)
	}
	for _, test := range []struct {
		query SessionQuery
		want  int
	}{
		{SessionQuery{}, 2},
		{SessionQuery{CreatedAfter: now.Add(-time.Minute).Format(time.RFC3339Nano), IncludeArchived: true}, 2},
		{SessionQuery{CreatedBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), IncludeArchived: true}, 1},
	} {
		page, err := mock.ListSessions(ctx, test.query)
		if err != nil || len(page.Data) != test.want {
			t.Fatalf("catalog query %+v: %+v %v", test.query, page, err)
		}
	}
	eventsPage, err := mock.ListEvents(ctx, first.ID, EventQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	more, err := mock.ListEvents(ctx, first.ID, EventQuery{Page: eventsPage.Data[0].ID})
	if err != nil || len(more.Data) != 0 {
		t.Fatalf("events page: %+v %v", more, err)
	}
	if _, err := mock.Status(ctx, "missing"); !IsStatus(err, 404) {
		t.Fatalf("missing status: %v", err)
	}
	if _, err := mock.ListEvents(ctx, "missing", EventQuery{}); !IsStatus(err, 404) {
		t.Fatalf("missing events: %v", err)
	}
	if err := mock.Interrupt(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := mock.ListSessions(canceled, SessionQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled catalog: %v", err)
	}
	if _, err := mock.ListFiles(canceled, first.ID, 1, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled files: %v", err)
	}
	for i := 3; i < 256; i++ {
		if _, err := mock.Create(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mock.Create(ctx); err == nil {
		t.Fatal("unbounded mock session creation")
	}
}
