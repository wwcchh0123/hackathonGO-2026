// Package mcp exposes the voice protocol through the official MCP SDK.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

const identityMeta = "linxi.control/request-identity"
const dedupWindow = 5 * time.Second

type identity struct{ device, conversation, agent string }
type dedupKey struct{ conversation, tool, text string }
type cached struct {
	receipt session.Receipt
	expires time.Time
}

func textResult(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}

func Handler(c *session.Controller) http.Handler {
	// KeepAlive stays disabled: Linx may not answer server-initiated MCP pings.
	s := mcp.NewServer(&mcp.Implementation{Name: "linxi-workstation", Version: "0.2.0"}, nil)
	var mu sync.Mutex
	recent := make(map[dedupKey]cached)
	submit := func(who identity, tool, text string) (session.Receipt, error, bool) {
		// Serialize only cache lookup and nonblocking queue admission, never cloud work.
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		for k, v := range recent {
			if !now.Before(v.expires) {
				delete(recent, k)
			}
		}
		key := dedupKey{who.conversation, tool, strings.TrimSpace(text)}
		if who.conversation != "" {
			if previous, ok := recent[key]; ok {
				return previous.receipt, nil, true
			}
		}
		action := "message"
		if tool == "start_new_project" {
			action = "start"
		}
		receipt, err := c.Submit(session.Command{Action: action, Prompt: text, Source: "mcp", ConversationID: who.conversation})
		if err == nil && who.conversation != "" {
			recent[key] = cached{receipt, now.Add(dedupWindow)}
		}
		return receipt, err, false
	}
	messageSchema := map[string]any{
		"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000, "description": "用户对编程搭档说的原话"}},
		"required": []string{"text"}, "additionalProperties": false,
	}
	descriptions := map[string]string{
		"send_message":      "默认入口。把用户对当前作品的需求、修改、继续、暂停原话转交；控制服务自行决定新建、复用或打断会话。",
		"start_new_project": "仅当用户明确要做一个与当前作品无关的新东西时使用；不确定时使用 send_message。",
		"get_status":        "查询进度、好了没或看看效果；只读当前状态，用一句中文如实转述。",
	}
	for _, name := range []string{"send_message", "start_new_project", "get_status"} {
		schema := messageSchema
		if name == "get_status" {
			schema = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		}
		s.AddTool(&mcp.Tool{Name: name, Description: descriptions[name], InputSchema: schema}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			started := time.Now()
			who := requestIdentity(r)
			duplicate := false
			result := textResult("工具参数不正确，请重新描述需求", true)
			defer func() {
				slog.Info("MCP tool call", "tool", name, "device_id", who.device, "linx_session_id", who.conversation, "linx_agent_id", who.agent, "duplicate", duplicate, "is_error", result.IsError, "duration_ms", time.Since(started).Milliseconds())
			}()
			var in struct {
				Text string `json:"text"`
			}
			arguments := r.Params.Arguments
			if len(arguments) == 0 {
				arguments = []byte("{}")
			}
			decoder := json.NewDecoder(bytes.NewReader(arguments))
			decoder.DisallowUnknownFields()
			if name == "get_status" {
				var empty struct{}
				if decoder.Decode(&empty) == nil && string(arguments) != "null" {
					result = textResult(c.StatusText(), false)
				}
			} else if decoder.Decode(&in) == nil {
				receipt, err, repeated := submit(who, name, in.Text)
				duplicate = repeated
				if err != nil {
					result = textResult(err.Error(), true)
				} else {
					result = textResult(receipt.Message, false)
				}
			}
			return result, nil
		})
	}
	h := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server { return s }, &mcp.SSEOptions{MaxRequestBodyBytes: 32 << 10})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("X-Accel-Buffering", "no")
			// The SDK advertises the GET path as its POST endpoint.
			r = r.Clone(r.Context())
			u := *r.URL
			u.Path = "/mcp/message"
			r.URL = &u
		} else if r.Method == http.MethodPost {
			// Legacy SSE in go-sdk does not populate RequestExtra.Header. Carry the
			// authenticated POST identity in private per-call metadata, overwriting any
			// client-supplied value. Never bind identity to the long-lived GET context.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
			if err != nil {
				http.Error(w, "请求过大或无法读取", http.StatusRequestEntityTooLarge)
				return
			}
			var envelope map[string]json.RawMessage
			var method string
			_ = json.Unmarshal(body, &envelope)
			_ = json.Unmarshal(envelope["method"], &method)
			if method == "tools/call" {
				var params map[string]json.RawMessage
				if json.Unmarshal(envelope["params"], &params) == nil && params != nil {
					var meta map[string]any
					_ = json.Unmarshal(params["_meta"], &meta)
					if meta == nil {
						meta = map[string]any{}
					}
					meta[identityMeta] = map[string]string{"device": r.Header.Get("X-Linx-Device-Id"), "conversation": r.Header.Get("X-Linx-Session-Id"), "agent": r.Header.Get("X-Linx-Agent-Id")}
					params["_meta"], _ = json.Marshal(meta)
					envelope["params"], _ = json.Marshal(params)
					body, _ = json.Marshal(envelope)
				}
			}
			r = r.Clone(r.Context())
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		h.ServeHTTP(w, r)
	})
}
func requestIdentity(r *mcp.CallToolRequest) identity {
	meta, _ := r.Params.Meta[identityMeta].(map[string]any)
	read := func(key string) string { v, _ := meta[key].(string); return v }
	return identity{read("device"), read("conversation"), read("agent")}
}
