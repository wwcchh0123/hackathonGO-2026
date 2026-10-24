package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestMCPStreamableLinxDiscoveryAndAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cfg := config.Config{Mode: "http", DeviceID: "test-device", Token: "test-control-token", DataDir: t.TempDir()}
	c := session.New(ctx, agentapi.NewMock(ctx), device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir})
	ts := httptest.NewServer(Handler(cfg, c))
	defer ts.Close()
	defer func() { cancel(); c.Wait() }()
	request := func(version, method string, params any, token, deviceID string, want int) json.RawMessage {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/mcp", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", token)
		req.Header.Set("X-Linx-Device-Id", deviceID)
		if method != "initialize" {
			req.Header.Set("Mcp-Protocol-Version", version)
		}
		if version == "2026-07-28" {
			req.Header.Set("Mcp-Method", method)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s device=%q: status %d want %d: %s", version, method, deviceID, resp.StatusCode, want, raw)
		}
		if want != http.StatusOK {
			return nil
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Error) > 0 || len(envelope.Result) == 0 {
			t.Fatalf("invalid RPC response: %s (%v)", raw, err)
		}
		return envelope.Result
	}
	// These are the versions observed from linxdv0 and xrobotd-manager.
	// Management calls have no device header; web previews have a virtual ID.
	for _, version := range []string{"2025-06-18", "2025-11-25"} {
		for _, deviceID := range []string{"", "web-preview-device"} {
			init := map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "linx-regression", "version": "1"}}
			request(version, "initialize", init, "Bearer test-control-token", deviceID, 200)
			raw := request(version, "tools/list", map[string]any{}, "Bearer test-control-token", deviceID, 200)
			var result mcp.ListToolsResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range result.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"get_status", "send_message", "start_new_project"}) {
				t.Fatal(names)
			}
			for _, name := range names {
				request(version, "tools/call", map[string]any{"name": name, "arguments": map[string]any{"text": "do not submit"}}, "Bearer test-control-token", deviceID, 403)
			}
		}
		request(version, "tools/list", map[string]any{}, "", "", 401)
		request(version, "tools/list", map[string]any{}, "Bearer wrong", "", 401)
		raw := request(version, "tools/call", map[string]any{"name": "get_status", "arguments": map[string]any{}}, "Bearer test-control-token", cfg.DeviceID, 200)
		var result mcp.CallToolResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if got := voiceResult(t, &result, false); got != "还没有任务，可以告诉我想做什么" {
			t.Fatal(got)
		}
	}
	// The manager also probes the newer discovery method before initialize.
	request("2026-07-28", "server/discover", map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": "xrobotd-manager", "version": "v1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"roots": map[string]bool{"listChanged": true}},
	}}, "Bearer test-control-token", "", 200)
	if c.State.Get().SessionID != "" {
		t.Fatal("metadata or unauthorized calls created a programming session")
	}
}
