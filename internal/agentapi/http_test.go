package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

func TestCloudContractAndUnmodifiedEvents(t *testing.T) {
	raw := `{"id":"evt_1","type":"agent.message","unknown":{"keep":true},"content":[{"type":"text","text":"你好"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("missing provider authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/sessions":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["agent"] != "agent_cc" || body["environment_id"] != "env_1" {
				t.Error("invalid session configuration")
			}
			fmt.Fprint(w, `{"id":"ses_1","status":"idle"}`)
		case "POST /v1/sessions/ses_1/events":
			var body struct {
				Events []struct {
					Type  string `json:"type"`
					Input struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"input"`
				} `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Events) != 1 {
				t.Error("expected one input")
				return
			}
			e := body.Events[0]
			if e.Type == "user.message" && (len(e.Input.Parts) != 1 || e.Input.Parts[0].Text != "hello") {
				t.Error("input.parts contract broken")
			}
			fmt.Fprint(w, `{}`)
		case "GET /v1/sessions/ses_1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, ": heartbeat\n\nevent: agent.message\ndata: %s\n\n", raw)
		case "GET /v1/files":
			if r.URL.Query().Get("scope_id") != "ses_1" {
				t.Error("file scope missing")
			}
			fmt.Fprint(w, `{"data":[{"id":"file_1","filename":"index.html"}]}`)
		case "GET /v1/files/file_1/content":
			fmt.Fprint(w, "<h1>preview</h1>")
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	p := NewHTTP(upstream.URL, "test-key", "agent_cc", "env_1")
	ctx := context.Background()
	s, err := p.Create(ctx)
	if err != nil || s.ID != "ses_1" {
		t.Fatalf("create: %v %v", s, err)
	}
	if err = p.Send(ctx, s.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	if err = p.Interrupt(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	var got events.Frame
	err = p.Stream(ctx, s.ID, func(f events.Frame) error { got = f; return nil })
	if err != io.EOF || string(got.Data) != raw || got.ID != "evt_1" {
		t.Fatalf("raw JSON changed: %q, %v", got.Data, err)
	}
	files, err := p.Artifacts(ctx, s.ID)
	if err != nil || len(files) != 1 || !strings.Contains(string(files[0].Data), "preview") {
		t.Fatalf("artifacts: %v", err)
	}
}
func TestProviderErrorsDoNotEchoSecrets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "secret-provider-body")
	}))
	defer upstream.Close()
	_, err := NewHTTP(upstream.URL, "secret-key", "a", "e").Create(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestPreviewUsesNewestExportAcrossFilePages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/files":
			if r.URL.Query().Get("page") == "older" {
				fmt.Fprint(w, `{"data":[{"id":"old","filename":"index.html","created_at":"2026-10-23T00:00:00Z"}]}`)
			} else {
				fmt.Fprint(w, `{"data":[{"id":"new","filename":"index.html","created_at":"2026-10-24T00:00:00Z"}],"next_page":"older"}`)
			}
		case "/v1/files/new/content":
			fmt.Fprint(w, "red button")
		default:
			t.Errorf("downloaded obsolete file %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	files, err := NewHTTP(upstream.URL, "test", "a", "e").Artifacts(context.Background(), "s")
	if err != nil || len(files) != 1 || string(files[0].Data) != "red button" {
		t.Fatalf("latest export lost: %v %v", files, err)
	}
}
