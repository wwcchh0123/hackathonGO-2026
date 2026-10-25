package config

import (
	"encoding/json"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
	"testing"
)

func TestRuntimeProfileConfigAndLegacyExclusion(t *testing.T) {
	for _, key := range []string{"AGENTAPI_AGENT_ID", "AGENTAPI_AGENT_VERSION", "AGENTAPI_ENVIRONMENT_ID", "AGENTAPI_VAULT_IDS", "AGENTAPI_DEFAULT_RUNTIME", "AGENTAPI_RUNTIME_PROFILES_JSON", "AGENTAPI_MODE", "DEV_TOOLS", "PUBLIC_URL"} {
		t.Setenv(key, "")
	}
	raw, _ := json.Marshal(runtimeprofile.MockRegistry())
	t.Setenv("AGENTAPI_RUNTIME_PROFILES_JSON", string(raw))
	c, err := Load()
	if err != nil || len(c.Profiles.Profiles) != 3 {
		t.Fatal(c, err)
	}
	for _, key := range []string{"AGENTAPI_AGENT_ID", "AGENTAPI_AGENT_VERSION", "AGENTAPI_ENVIRONMENT_ID", "AGENTAPI_VAULT_IDS"} {
		t.Setenv(key, "1")
		if _, err := Load(); err == nil {
			t.Fatal("accepted mixed config", key)
		}
		t.Setenv(key, "")
	}
	t.Setenv("AGENTAPI_DEFAULT_RUNTIME", "missing")
	if _, err := Load(); err == nil {
		t.Fatal("invalid default")
	}
	t.Setenv("AGENTAPI_DEFAULT_RUNTIME", "")
	for _, raw := range []string{" ", "{"} {
		t.Setenv("AGENTAPI_RUNTIME_PROFILES_JSON", raw)
		if _, err := Load(); err == nil {
			t.Fatal("accepted bad profiles")
		}
	}
	t.Setenv("AGENTAPI_RUNTIME_PROFILES_JSON", "")
	t.Setenv("AGENTAPI_AGENT_ID", "a")
	if _, err := Load(); err == nil {
		t.Fatal("missing legacy env")
	}
	t.Setenv("AGENTAPI_AGENT_ID", "")
	t.Setenv("AGENTAPI_MODE", "http")
	t.Setenv("AGENTAPI_RUNTIME_PROFILES_JSON", string(raw))
	if _, err := Load(); err == nil {
		t.Fatal("missing http credentials")
	}
}
