package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfileFixedVersionOverrideAndDecode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Agent struct {
				ID, Type string
				Version  int
				Model    struct{ ID, Effort string }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Agent.Type != "agent_with_overrides" || body.Agent.Version != 7 || body.Agent.Model.ID != "model" || body.Agent.Model.Effort != "high" {
			t.Errorf("wrong override: %+v", body)
		}
		fmt.Fprint(w, `{"id":"s","status":"idle","agent":{"id":"a","version":7,"runtime":"codex","model":{"id":"model","effort":{"type":"high"}}}}`)
	}))
	defer upstream.Close()
	p := NewHTTP(upstream.URL, "key", "a", "e").WithProfile(runtimeprofile.Profile{AgentID: "a", AgentVersion: 7, EnvironmentID: "e", Runtime: "codex"})
	s, err := p.Create(context.Background(), NewSession{Model: Model{ID: "model", Effort: "high"}})
	if err != nil || s.Model.Effort != "high" || s.AgentVersion != 7 {
		t.Fatal(s, err)
	}
	for _, test := range []struct {
		raw  string
		want Model
	}{{`{"id":"m","effort":"low"}`, Model{ID: "m", Effort: "low"}}, {`{"id":"m","effort":{"type":"high"}}`, Model{ID: "m", Effort: "high"}}, {`invalid`, Model{}}} {
		if got := decodeModel([]byte(test.raw)); got != test.want {
			t.Fatal(got)
		}
	}
}
func TestValidateEveryPinnedProfileResource(t *testing.T) {
	for _, scenario := range []string{"ok", "wrong-id", "wrong-version", "wrong-runtime", "wrong-model", "wrong-effort", "bad-allowed-model", "bad-allowed-effort", "vault-error", "archived-vault", "wrong-vault", "agent-error"} {
		t.Run(scenario, func(t *testing.T) {
			profile := runtimeprofile.Profile{AgentID: "a", AgentVersion: 7, EnvironmentID: "e", Runtime: "native", DefaultModel: runtimeprofile.Model{ID: "m", Effort: "high"}, AllowedModels: []runtimeprofile.Model{{ID: "m", Effort: "high"}}, VaultIDs: []string{"v"}}
			if scenario == "bad-allowed-model" {
				profile.AllowedModels[0].ID = ""
			}
			if scenario == "bad-allowed-effort" {
				profile.AllowedModels[0].Effort = "bad"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/agents/a":
					if r.URL.Query().Get("version") != "7" {
						t.Error("unpinned validation")
					}
					if scenario == "agent-error" {
						w.WriteHeader(503)
						return
					}
					id, runtime, model, effort, version := "a", "native", "m", "high", 7
					switch scenario {
					case "wrong-id":
						id = "other"
					case "wrong-version":
						version = 8
					case "wrong-runtime":
						runtime = "codex"
					case "wrong-model":
						model = "other"
					case "wrong-effort":
						effort = "low"
					}
					fmt.Fprintf(w, `{"id":%q,"type":"agent","version":%d,"runtime":%q,"model":{"id":%q,"effort":{"type":%q}}}`, id, version, runtime, model, effort)
				case "/v1/environments":
					fmt.Fprint(w, `{"data":[{"id":"e","config":{"type":"cloud"}}]}`)
				case "/v1/vaults/v":
					switch scenario {
					case "vault-error":
						w.WriteHeader(403)
					case "archived-vault":
						fmt.Fprint(w, `{"id":"v","type":"vault","archived_at":"2026-10-24T00:00:00Z"}`)
					case "wrong-vault":
						fmt.Fprint(w, `{"id":"other","type":"vault"}`)
					default:
						fmt.Fprint(w, `{"id":"v","type":"vault"}`)
					}
				default:
					t.Error(r.URL.Path)
				}
			}))
			defer upstream.Close()
			p := NewHTTP(upstream.URL, "key", "a", "e").WithProfile(profile)
			err := p.ValidateProfile(context.Background(), profile)
			if (err == nil) != (scenario == "ok") {
				t.Fatal(err)
			}
			if err == nil && p.EffectiveModel().Effort != "high" {
				t.Fatal(p.EffectiveModel())
			}
		})
	}
}
func TestProfileSessionListSkipsOtherVersions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "older" {
			fmt.Fprint(w, `{"data":[{"id":"wanted","agent":{"version":7,"runtime":"codex"}}]}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"other","agent":{"version":8,"runtime":"codex"}}],"next_page":"older"}`)
		}
	}))
	defer upstream.Close()
	page, err := NewHTTP(upstream.URL, "key", "a", "e").WithVersion(7).ListSessions(context.Background(), SessionQuery{Limit: 1})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != "wanted" {
		t.Fatal(page, err)
	}
}
