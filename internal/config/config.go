package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

type Config struct {
	Addr, Mode, DeviceID, PublicURL, DataDir, Token, AgentURL, AgentKey, AgentID, EnvironmentID string
	DevTools                                                                                    bool
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func Load() (Config, error) {
	c := Config{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Mode: env("AGENTAPI_MODE", "mock"), DeviceID: env("DEVICE_ID", "demo-device"), PublicURL: env("PUBLIC_URL", "http://localhost:8080"), DataDir: env("DATA_DIR", ".data/results"), Token: os.Getenv("CONTROL_TOKEN"), AgentURL: os.Getenv("AGENTAPI_BASE_URL"), AgentKey: os.Getenv("AGENTAPI_API_KEY"), AgentID: os.Getenv("AGENTAPI_AGENT_ID"), EnvironmentID: os.Getenv("AGENTAPI_ENVIRONMENT_ID")}
	var err error
	c.DevTools, err = strconv.ParseBool(env("DEV_TOOLS", strconv.FormatBool(c.Mode == "mock")))
	if err != nil {
		return c, fmt.Errorf("DEV_TOOLS must be true or false")
	}
	if c.Mode != "mock" && c.Mode != "http" {
		return c, fmt.Errorf("AGENTAPI_MODE must be mock or http")
	}
	if c.Mode == "http" && (c.AgentURL == "" || c.AgentKey == "" || c.AgentID == "" || c.EnvironmentID == "" || c.Token == "") {
		return c, fmt.Errorf("http mode requires AGENTAPI_BASE_URL, AGENTAPI_API_KEY, AGENTAPI_AGENT_ID, AGENTAPI_ENVIRONMENT_ID and CONTROL_TOKEN")
	}
	for name, raw := range map[string]string{"PUBLIC_URL": c.PublicURL, "AGENTAPI_BASE_URL": c.AgentURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("invalid %s", name)
		}
	}
	return c, nil
}
