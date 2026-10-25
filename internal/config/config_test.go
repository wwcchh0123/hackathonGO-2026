package config

import "testing"

func TestLoadConfigurationAndVaultReferences(t *testing.T) {
	for _, key := range []string{"LISTEN_ADDR", "PUBLIC_URL", "DEVICE_ID", "AGENTAPI_MODE", "DEV_TOOLS", "DATA_DIR", "CONTROL_TOKEN", "AGENTAPI_BASE_URL", "AGENTAPI_API_KEY", "AGENTAPI_AGENT_ID", "AGENTAPI_AGENT_VERSION", "AGENTAPI_ENVIRONMENT_ID", "AGENTAPI_VAULT_IDS"} {
		t.Setenv(key, "")
	}
	c, err := Load()
	if err != nil || c.Mode != "mock" || !c.DevTools {
		t.Fatal(c, err)
	}
	t.Setenv("AGENTAPI_VAULT_IDS", " v1, ,v2 ")
	c, err = Load()
	if err != nil || len(c.VaultIDs) != 2 || c.VaultIDs[1] != "v2" {
		t.Fatal(c, err)
	}
	for _, test := range []struct{ key, value string }{{"AGENTAPI_MODE", "invalid"}, {"DEV_TOOLS", "invalid"}, {"AGENTAPI_AGENT_VERSION", "0"}, {"AGENTAPI_AGENT_VERSION", "abc"}, {"PUBLIC_URL", "https://user:pass@example.test"}, {"PUBLIC_URL", "https://example.test?secret=x"}, {"PUBLIC_URL", "://"}} {
		t.Setenv(test.key, test.value)
		if _, err := Load(); err == nil {
			t.Fatal("accepted invalid", test)
		}
		t.Setenv(test.key, "")
	}
	t.Setenv("AGENTAPI_MODE", "http")
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:18080")
	for _, key := range []string{"CONTROL_TOKEN", "AGENTAPI_API_KEY", "AGENTAPI_AGENT_ID", "AGENTAPI_ENVIRONMENT_ID"} {
		t.Setenv(key, "test")
	}
	t.Setenv("AGENTAPI_BASE_URL", "https://agent.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("accepted loopback PUBLIC_URL in http mode")
	}
	for _, key := range []string{"CONTROL_TOKEN", "AGENTAPI_API_KEY", "AGENTAPI_AGENT_ID", "AGENTAPI_ENVIRONMENT_ID", "AGENTAPI_BASE_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("PUBLIC_URL", "")
	t.Setenv("AGENTAPI_MODE", "http")
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing HTTP configuration")
	}
	for _, key := range []string{"CONTROL_TOKEN", "AGENTAPI_API_KEY", "AGENTAPI_AGENT_ID", "AGENTAPI_ENVIRONMENT_ID"} {
		t.Setenv(key, "test")
	}
	t.Setenv("AGENTAPI_BASE_URL", "https://agent.example.test")
	t.Setenv("PUBLIC_URL", "https://display.example.test")
	t.Setenv("AGENTAPI_AGENT_VERSION", "7")
	t.Setenv("DEV_TOOLS", "false")
	c, err = Load()
	if err != nil || c.AgentVersion != 7 || c.DevTools {
		t.Fatal(c, err)
	}
}
