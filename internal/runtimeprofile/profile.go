// Package runtimeprofile defines the immutable runtime/model profiles used by
// the control plane. A profile is the only place where an AgentAPI Agent,
// version, environment and model allowlist are selected.
package runtimeprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

var validRuntimes = map[string]bool{"claudecode": true, "codex": true, "native": true}
var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

type Model struct {
	ID     string `json:"id"`
	Effort string `json:"effort,omitempty"`
}

type Capabilities struct {
	Continue  bool `json:"continue"`
	Interrupt bool `json:"interrupt"`
	Events    bool `json:"events"`
	Files     bool `json:"files"`
	Preview   bool `json:"preview"`
}

type Profile struct {
	Key             string       `json:"key"`
	Runtime         string       `json:"runtime"`
	AgentID         string       `json:"agent_id"`
	AgentVersion    int          `json:"agent_version"`
	EnvironmentID   string       `json:"environment_id"`
	VaultIDs        []string     `json:"vault_ids,omitempty"`
	DefaultModel    Model        `json:"default_model"`
	AllowedModels   []Model      `json:"allowed_models"`
	SharedResources bool         `json:"shared_resources,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
	CreateEnabled   bool         `json:"create_enabled"`
	ReadEnabled     bool         `json:"read_enabled"`
}

type Registry struct {
	Profiles       []Profile `json:"profiles"`
	DefaultRuntime string    `json:"default_runtime,omitempty"`
}

func allCapabilities() Capabilities {
	return Capabilities{Continue: true, Interrupt: true, Events: true, Files: true, Preview: true}
}

// MockRegistry gives the mock service the same three selectable runtimes as a
// production deployment. The IDs are intentionally local fixture IDs.
func MockRegistry() Registry {
	return Registry{
		DefaultRuntime: "claudecode",
		Profiles: []Profile{
			{Key: "claudecode", Runtime: "claudecode", AgentID: "mock-claudecode", AgentVersion: 1, EnvironmentID: "mock-environment", SharedResources: true, DefaultModel: Model{ID: "mock/claudecode"}, AllowedModels: []Model{{ID: "mock/claudecode"}}, Capabilities: allCapabilities(), CreateEnabled: true, ReadEnabled: true},
			{Key: "codex", Runtime: "codex", AgentID: "mock-codex", AgentVersion: 1, EnvironmentID: "mock-environment", SharedResources: true, DefaultModel: Model{ID: "mock/codex"}, AllowedModels: []Model{{ID: "mock/codex"}}, Capabilities: allCapabilities(), CreateEnabled: true, ReadEnabled: true},
			{Key: "native", Runtime: "native", AgentID: "mock-native", AgentVersion: 1, EnvironmentID: "mock-environment", SharedResources: true, DefaultModel: Model{ID: "mock/native"}, AllowedModels: []Model{{ID: "mock/native"}}, Capabilities: allCapabilities(), CreateEnabled: true, ReadEnabled: true},
		},
	}
}

func Legacy(agentID string, version int, environment string, vaults []string) Registry {
	return Registry{DefaultRuntime: "claudecode", Profiles: []Profile{{
		Key: "claudecode", Runtime: "claudecode", AgentID: agentID, AgentVersion: version,
		EnvironmentID: environment, VaultIDs: append([]string(nil), vaults...),
		Capabilities: allCapabilities(), CreateEnabled: true, ReadEnabled: true,
	}}}
}

func Parse(raw string) (Registry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Registry{}, errors.New("AGENTAPI_RUNTIME_PROFILES_JSON is empty")
	}
	if err := requireProfileFields(raw); err != nil {
		return Registry{}, err
	}
	var registry Registry
	if strings.HasPrefix(raw, "[") {
		if err := strictJSON(raw, &registry.Profiles); err != nil {
			return Registry{}, fmt.Errorf("invalid AGENTAPI_RUNTIME_PROFILES_JSON: %w", err)
		}
	} else if err := strictJSON(raw, &registry); err != nil {
		return Registry{}, fmt.Errorf("invalid AGENTAPI_RUNTIME_PROFILES_JSON: %w", err)
	}
	for _, p := range registry.Profiles {
		if p.DefaultModel.ID == "" {
			return Registry{}, fmt.Errorf("profile %q requires default_model", p.Key)
		}
	}
	if err := registry.Validate(); err != nil {
		return Registry{}, err
	}
	if registry.DefaultRuntime == "" {
		for _, p := range registry.Profiles {
			if p.CreateEnabled {
				registry.DefaultRuntime = p.Key
				break
			}
		}
	}
	return registry, nil
}

func requireProfileFields(raw string) error {
	var entries []map[string]json.RawMessage
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return fmt.Errorf("invalid AGENTAPI_RUNTIME_PROFILES_JSON: %w", err)
		}
	} else {
		var envelope struct {
			Profiles []map[string]json.RawMessage `json:"profiles"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return fmt.Errorf("invalid AGENTAPI_RUNTIME_PROFILES_JSON: %w", err)
		}
		entries = envelope.Profiles
	}
	for i, entry := range entries {
		for _, field := range []string{"capabilities", "create_enabled", "read_enabled"} {
			if _, ok := entry[field]; !ok {
				return fmt.Errorf("profile %d requires %s", i, field)
			}
		}
	}
	return nil
}

func (r Registry) Validate() error {
	if len(r.Profiles) > 16 {
		return errors.New("runtime profile registry supports at most 16 profiles")
	}
	if len(r.Profiles) == 0 {
		return errors.New("runtime profile registry is empty")
	}
	seenKey, creates, snapshots := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range r.Profiles {
		p := &r.Profiles[i]
		if p.Key == "" {
			p.Key = p.Runtime
		}
		if p.Key == "" || len(p.Key) > 64 || strings.ContainsAny(p.Key, "\r\n/\\") {
			return fmt.Errorf("profile %d has invalid key", i)
		}
		if !validRuntimes[p.Runtime] {
			return fmt.Errorf("profile %q has unsupported runtime %q", p.Key, p.Runtime)
		}
		if validRuntimes[p.Key] && p.Key != p.Runtime {
			return fmt.Errorf("profile key %q is reserved for runtime %q", p.Key, p.Key)
		}
		if seenKey[p.Key] || (p.CreateEnabled && creates[p.Runtime]) {
			return fmt.Errorf("duplicate runtime profile %q", p.Key)
		}
		seenKey[p.Key] = true
		if p.CreateEnabled {
			creates[p.Runtime] = true
		}
		if p.AgentID == "" || p.EnvironmentID == "" || p.AgentVersion < 1 {
			return fmt.Errorf("profile %q requires agent_id, positive agent_version and environment_id", p.Key)
		}
		snapshot := fmt.Sprintf("%s:%d:%s", p.AgentID, p.AgentVersion, p.EnvironmentID)
		if snapshots[snapshot] {
			return fmt.Errorf("duplicate Agent/version/environment profile %q", p.Key)
		}
		snapshots[snapshot] = true
		if !p.CreateEnabled && !p.ReadEnabled {
			return fmt.Errorf("profile %q must allow create or read", p.Key)
		}
		if err := validateModel(p.DefaultModel, p.Key, true); err != nil {
			return err
		}
		if len(p.AllowedModels) == 0 && p.CreateEnabled && p.DefaultModel.ID != "" {
			p.AllowedModels = []Model{p.DefaultModel}
		}
		for _, model := range p.AllowedModels {
			if err := validateModel(model, p.Key, false); err != nil {
				return err
			}
		}
		if p.DefaultModel.ID != "" && !containsModel(p.AllowedModels, p.DefaultModel) {
			return fmt.Errorf("profile %q default_model is not in allowed_models", p.Key)
		}
	}
	if err := r.ValidateSharing(); err != nil {
		return err
	}
	if r.DefaultRuntime != "" {
		_, ok := r.ResolveDefault(r.DefaultRuntime)
		if !ok {
			return fmt.Errorf("default runtime %q is not a create-enabled profile", r.DefaultRuntime)
		}
	}
	return nil
}

func validateModel(model Model, key string, optional bool) error {
	if model.ID == "" && optional {
		return nil
	}
	if model.ID == "" || len(model.ID) > 256 || strings.ContainsAny(model.ID, "\r\n") {
		return fmt.Errorf("profile %q has invalid model id", key)
	}
	if model.Effort != "" && !validEfforts[model.Effort] {
		return fmt.Errorf("profile %q has invalid model effort %q", key, model.Effort)
	}
	return nil
}

func containsModel(models []Model, want Model) bool {
	for _, model := range models {
		if model.ID == want.ID && model.Effort == want.Effort {
			return true
		}
	}
	return false
}

func (r Registry) Lookup(key string) (Profile, bool) {
	for _, p := range r.Profiles {
		if p.Key == key {
			return p, true
		}
	}
	return Profile{}, false
}

// ResolveDefault accepts a runtime name or an explicit profile key. Runtime
// names always select the create-enabled version, including during rotation.
func (r Registry) ResolveDefault(selection string) (Profile, bool) {
	if validRuntimes[selection] {
		for _, p := range r.Profiles {
			if p.Runtime == selection && p.CreateEnabled {
				return p, true
			}
		}
		return Profile{}, false
	}
	p, ok := r.Lookup(selection)
	return p, ok && p.CreateEnabled
}

func (r Registry) Keys() []string {
	keys := make([]string, 0, len(r.Profiles))
	for _, p := range r.Profiles {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return keys
}

func (p Profile) AllowsModel(model Model) bool {
	if model.ID == "" {
		return true
	}
	return containsModel(p.AllowedModels, model)
}

func (p Profile) EffectiveModel(requested Model) Model {
	if requested.ID != "" {
		return requested
	}
	return p.DefaultModel
}

func strictJSON(raw string, value any) error {
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("only one JSON value is allowed")
	}
	return nil
}

// ValidateSharing requires both profiles to opt in before sharing a credential
// or execution environment. Version rotation of the same runtime is exempt.
func (r Registry) ValidateSharing() error {
	for i, a := range r.Profiles {
		for _, b := range r.Profiles[i+1:] {
			if a.Runtime == b.Runtime {
				continue
			}
			shared := a.EnvironmentID == b.EnvironmentID
			for _, x := range a.VaultIDs {
				for _, y := range b.VaultIDs {
					if x == y {
						shared = true
					}
				}
			}
			if shared && (!a.SharedResources || !b.SharedResources) {
				return fmt.Errorf("profiles %q and %q share resources without mutual shared_resources opt-in", a.Key, b.Key)
			}
		}
	}
	return nil
}
