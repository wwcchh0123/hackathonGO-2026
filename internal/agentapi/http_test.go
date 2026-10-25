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
		w.Header().Set("Content-Type", "application/json")
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

func TestPinnedAgentVersionUsesSDKAgentReference(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		agent, ok := body["agent"].(map[string]any)
		if !ok || agent["id"] != "agent" || agent["version"] != float64(7) || agent["type"] != "agent" {
			t.Fatalf("unversioned agent reference: %#v", body["agent"])
		}
		fmt.Fprint(w, `{"id":"ses_7","status":"idle"}`)
	}))
	defer upstream.Close()
	if _, err := NewHTTP(upstream.URL, "key", "agent", "env").WithVersion(7).Create(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewUsesNewestExportAcrossFilePages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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

func TestSessionCatalogSDKContractProjectionAndVaults(t *testing.T) {
	ctx := context.Background()
	rawSession := `{"id":"ses_catalog","status":"idle","title":"historical","agent":{"runtime":"claudecode","system":"private-system"},"metadata":{"source":"linxi-control"},"resources":[{"authorization_token":"private-token"}],"vault_ids":["vlt_private"],"created_at":"2026-10-24T00:00:00Z"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/sessions":
			q := r.URL.Query()
			if q.Get("status") != "idle" || q.Get("agent_id") != "agent" || q.Get("page") != "cursor" || q.Get("created_at[gte]") == "" || q.Has("statuses[]") || q.Has("order") {
				t.Errorf("wrong list contract %v", q)
			}
			fmt.Fprintf(w, `{"data":[%s],"next_page":"next"}`, rawSession)
		case "GET /v1/sessions/ses_catalog":
			fmt.Fprint(w, rawSession)
		case "GET /v1/sessions/ses_catalog/events":
			q := r.URL.Query()
			if q.Get("order") != "desc" || q.Get("page") != "older" || q.Get("types[]") != "agent.message" {
				t.Errorf("wrong event query %v", q)
			}
			fmt.Fprint(w, `{"data":[{"id":"evt_raw","type":"agent.message","custom":{"kept":true},"content":[{"type":"text","text":"hello"}]}],"next_page":"more"}`)
		case "GET /v1/files":
			if r.URL.Query().Get("scope_id") != "ses_catalog" || r.URL.Query().Get("page") != "filepage" {
				t.Error(r.URL.Query())
			}
			fmt.Fprint(w, `{"data":[{"id":"file","filename":"notes.md","size_bytes":42}],"next_page":"file-next"}`)
		case "POST /v1/sessions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["title"] != "title" || len(body["vault_ids"].([]any)) != 2 || body["metadata"].(map[string]any)["client_request_id"] != "cmd_test" {
				t.Errorf("missing creation data %#v", body)
			}
			if _, exists := body["resources"]; exists {
				t.Error("fixed repository injected")
			}
			fmt.Fprint(w, rawSession)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	p := NewHTTP(upstream.URL, "test-key", "agent", "env").WithVaults([]string{"v1", "v2"})
	page, err := p.ListSessions(ctx, SessionQuery{Limit: 2, Page: "cursor", AgentID: "unrelated-agent", Status: "idle", CreatedAfter: "2026-10-01T00:00:00Z", CreatedBefore: "2026-10-25T00:00:00Z", IncludeArchived: true})
	if err != nil || len(page.Data) != 1 || page.NextPage != "next" {
		t.Fatal(page, err)
	}
	s, err := p.Status(ctx, "ses_catalog")
	if err != nil || s.Runtime != "claudecode" || s.Metadata["source"] != "linxi-control" {
		t.Fatal(s, err)
	}
	data, _ := json.Marshal(s)
	for _, secret := range []string{"private-system", "private-token", "vlt_private", "metadata"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("projection leaked", secret)
		}
	}
	es, err := p.ListEvents(ctx, s.ID, EventQuery{Limit: 8, Order: "desc", Page: "older", Types: []string{"agent.message"}})
	if err != nil || es.NextPage != "more" || !strings.Contains(string(es.Data[0].Data), "custom") {
		t.Fatal(es, err)
	}
	fs, err := p.ListFiles(ctx, s.ID, 50, "filepage")
	if err != nil || fs.NextPage != "file-next" || fs.Data[0].Size != 42 {
		t.Fatal(fs, err)
	}
	if _, err = p.Create(ctx, NewSession{Title: "title", Metadata: map[string]string{"client_request_id": "cmd_test"}}); err != nil {
		t.Fatal(err)
	}
}
func TestWriteErrorUncertaintyAndValidation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 408, 409, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private-key-in-body")
			}))
			defer upstream.Close()
			p := NewHTTP(upstream.URL, "private-key", "a", "e")
			_, err := p.Create(context.Background())
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
			want := status == 408 || status >= 500
			if Uncertain(err) != want {
				t.Fatalf("%d ambiguity %v", status, Uncertain(err))
			}
		})
	}
	for _, runtime := range []string{"claudecode", "native"} {
		t.Run(runtime, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "agents") {
					fmt.Fprintf(w, `{"id":"a","type":"agent","runtime":%q}`, runtime)
				} else {
					fmt.Fprint(w, `{"id":"e","config":{"type":"cloud"}}`)
				}
			}))
			defer upstream.Close()
			err := NewHTTP(upstream.URL, "key", "a", "e").Validate(context.Background())
			if (err == nil) != (runtime == "claudecode") {
				t.Fatal(err)
			}
		})
	}
}
