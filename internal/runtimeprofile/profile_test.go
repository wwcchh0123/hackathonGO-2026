package runtimeprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryConfigurationAndRotation(t *testing.T) {
	r := MockRegistry()
	raw, _ := json.Marshal(r)
	parsed, err := Parse(string(raw))
	if err != nil || len(parsed.Profiles) != 3 {
		t.Fatal(parsed, err)
	}
	raw, _ = json.Marshal(r.Profiles)
	parsed, err = Parse(string(raw))
	if err != nil || parsed.DefaultRuntime != "claudecode" {
		t.Fatal(parsed, err)
	}
	old := r.Profiles[1]
	old.Key = "codex-old"
	old.AgentVersion = 2
	old.CreateEnabled = false
	r.Profiles = append(r.Profiles, old)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if p, ok := r.Lookup("codex-old"); !ok || p.AgentVersion != 2 {
		t.Fatal(p, ok)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("unknown lookup")
	}
	if len(r.Keys()) != 4 {
		t.Fatal(r.Keys())
	}
	p := r.Profiles[0]
	if !p.AllowsModel(Model{}) || p.AllowsModel(Model{ID: "missing"}) || p.EffectiveModel(Model{}).ID == "" || p.EffectiveModel(Model{ID: "x"}).ID != "x" {
		t.Fatal(p)
	}
	legacy := Legacy("a", 3, "e", []string{"v"})
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestRegistryRejectsInvalidConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{"profiles":[{"key":"codex","runtime":"codex","agent_id":"a","agent_version":1,"environment_id":"e","default_model":{"id":"m"},"allowed_models":[{"id":"m"}],"create_enabled":true,"read_enabled":true}]}`,
		`{"profiles":[{"key":"codex","runtime":"codex","agent_id":"a","agent_version":1,"environment_id":"e","default_model":{"id":"m"},"allowed_models":[{"id":"m"}],"capabilities":{"continue":true},"read_enabled":true}]}`,
	} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted profile with missing required fields: %s", raw)
		}
	}
	for _, raw := range []string{"", "{", "[]", "null", `{"profiles":[],"typo":1}`, "[] {}", "[{}]"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, test := range []struct {
		name   string
		change func(*Registry)
	}{
		{"key", func(r *Registry) { r.Profiles[0].Key = "x/y" }},
		{"runtime", func(r *Registry) { r.Profiles[0].Runtime = "unknown" }},
		{"duplicate", func(r *Registry) { r.Profiles = append(r.Profiles, r.Profiles[0]) }},
		{"agent", func(r *Registry) { r.Profiles[0].AgentID = "" }},
		{"version", func(r *Registry) { r.Profiles[0].AgentVersion = 0 }},
		{"disabled", func(r *Registry) { r.Profiles[0].CreateEnabled = false; r.Profiles[0].ReadEnabled = false }},
		{"default", func(r *Registry) { r.DefaultRuntime = "absent" }},
		{"effort", func(r *Registry) { r.Profiles[0].DefaultModel.Effort = "unknown" }},
		{"model", func(r *Registry) { r.Profiles[0].AllowedModels = []Model{{ID: ""}} }},
		{"model-long", func(r *Registry) { r.Profiles[0].AllowedModels = []Model{{ID: strings.Repeat("m", 257)}} }},
		{"model-effort", func(r *Registry) { r.Profiles[0].AllowedModels = []Model{{ID: "m", Effort: "bad"}} }},
		{"allowlist", func(r *Registry) { r.Profiles[0].AllowedModels = []Model{{ID: "other"}} }},
		{"sharing-env", func(r *Registry) { r.Profiles[0].SharedResources = false }},
		{"sharing-vault", func(r *Registry) {
			r.Profiles[0].EnvironmentID = "a"
			r.Profiles[1].EnvironmentID = "b"
			r.Profiles[0].VaultIDs = []string{"v"}
			r.Profiles[1].VaultIDs = []string{"v"}
			r.Profiles[1].SharedResources = false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := MockRegistry()
			test.change(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid registry accepted")
			}
		})
	}
	r := MockRegistry()
	r.Profiles[0].Key = ""
	r.Profiles[0].AllowedModels = nil
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeNamesAreReservedAndRotationSelectsCreatableVersion(t *testing.T) {
	r := MockRegistry()
	r.Profiles[1].Key = "native"
	r.Profiles[2].Key = "native-v2"
	if err := r.Validate(); err == nil {
		t.Fatal("cross-runtime reserved key accepted")
	}
	r = MockRegistry()
	old := r.Profiles[1]
	old.CreateEnabled = false
	current := old
	current.Key = "codex-v2"
	current.AgentVersion = 2
	current.CreateEnabled = true
	r.Profiles[1] = old
	r.Profiles = append(r.Profiles, current)
	r.DefaultRuntime = "codex"
	if err := r.Validate(); err != nil {
		t.Fatal("read-only old profile blocks rotation", err)
	}
	for _, selection := range []string{"codex", "codex-v2"} {
		p, ok := r.ResolveDefault(selection)
		if !ok || p.Key != "codex-v2" {
			t.Fatal(selection, p, ok)
		}
	}
	if p, ok := r.Lookup("codex"); !ok || p.CreateEnabled {
		t.Fatal("exact lookup must retain old read-only profile", p, ok)
	}
	if _, ok := r.ResolveDefault("missing"); ok {
		t.Fatal("unknown default accepted")
	}
	r.Profiles[3].CreateEnabled = false
	if _, ok := r.ResolveDefault("codex"); ok {
		t.Fatal("disabled runtime default accepted")
	}
	if _, ok := r.ResolveDefault("codex-v2"); ok {
		t.Fatal("disabled profile default accepted")
	}
}
