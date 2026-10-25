// Package mcp exposes the voice protocol through the official MCP SDK.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

const identityMeta = "linxi.control/request-identity"

type identity struct{ device, conversation, agent string }

func textResult(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}
func errorResult(err error) *mcp.CallToolResult {
	value := session.ErrorResponse(err)
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return textResult(`{"code":"request_failed"}`, true)
	}
	return textResult(string(raw), true)
}
func decodeArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return io.ErrUnexpectedEOF
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// HandlerFor keeps the MCP wire protocol independent from the control-plane
// registry. The resolver is called only for tools/call and may route each
// authenticated device to its own session controller.
func HandlerFor(resolve func(string) *session.Controller, publicURL string) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "linxi-workstation", Version: "0.3.0"}, nil)
	text := map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}
	ref := map[string]any{"type": "string", "minLength": 1, "maxLength": 100}
	str := map[string]any{"type": "string", "maxLength": 512}
	model := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "effort": map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "xhigh", "max"}}}, "required": []string{"id"}, "additionalProperties": false}
	schemas := map[string]map[string]any{
		"new_task":     {"text": text, "runtime": str, "model": model, "parent_task_ref": ref},
		"send_message": {"text": text, "task_ref": ref, "runtime": str, "model": model},
		"list_tasks":   {"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "page": map[string]any{"type": "string", "maxLength": 32768}, "runtime": str, "model": model, "status": map[string]any{"type": "string", "enum": []string{"idle", "running", "rescheduling", "terminated"}}, "include_archived": map[string]any{"type": "boolean"}, "created_after": str, "created_before": str},
		"get_task":     {"task_ref": ref, "command_id": str, "events_limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "events_page": str, "files_page": str},
	}
	descriptions := map[string]string{
		"new_task":     "明确新建独立任务；runtime 可选 claudecode/codex/native，model 只能来自已配置 allowlist。runtime/model 改变必须 new_task，可传 parent_task_ref 接力。不停止其他任务。返回 task_ref、command_id 和可直接投屏的 display_url，accepted 仅表示已入队。接力历史任务时明确传 parent_task_ref。",
		"send_message": "runtime/model 仅校验与原任务一致，不切换运行时。向明确的 task_ref 原话发送需求、修改、继续或暂停。省略引用仅复用同一灵矽对话最近一次写入目标；查询不改变目标。返回异步回执和 display_url，请用 get_task 核对 command_id。",
		"list_tasks":   "只读合并所有可读 runtime profile 的任务，可按 runtime/model 过滤；partial=true 时说明部分 runtime 不可查询，不能声称完整。按最近创建排序，读取单页；has_more 为真时不能声称已列出全部。按标题、时间和摘要选择 task_ref，歧义时询问用户。",
		"get_task":     "只读任务详情，包含云端状态、命令结果、消息时间、停止原因、事件、文件元信息和可投屏 display_url。events_limit=0 只查状态和消息，不建事件流、不切屏、不下载预览。旧摘要不是本轮完成证明。",
	}
	for _, name := range []string{"list_tasks", "get_task", "send_message", "new_task"} {
		required := []string{}
		if name == "get_task" {
			required = []string{"task_ref"}
		}
		if name == "new_task" || name == "send_message" {
			required = []string{"text"}
		}
		s.AddTool(&mcp.Tool{Name: name, Description: descriptions[name], InputSchema: map[string]any{"type": "object", "properties": schemas[name], "required": required, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			started := time.Now()
			who := requestIdentity(r)
			var value any
			var err error
			switch name {
			case "list_tasks":
				var in session.ListOptions
				err = decodeArgs(r.Params.Arguments, &in)
				if err == nil {
					controller := resolve(who.device)
					if controller == nil {
						err = fmt.Errorf("未知设备")
					} else {
						value, err = controller.ListTasks(ctx, in)
					}
				}
			case "get_task":
				var in struct {
					TaskRef string `json:"task_ref"`
					session.DetailOptions
				}
				err = decodeArgs(r.Params.Arguments, &in)
				if err == nil {
					controller := resolve(who.device)
					if controller == nil {
						err = fmt.Errorf("未知设备")
					} else {
						value, err = controller.GetTask(ctx, in.TaskRef, in.DetailOptions)
					}
				}
			default:
				var in struct {
					Text      string         `json:"text"`
					TaskRef   string         `json:"task_ref,omitempty"`
					ParentRef string         `json:"parent_task_ref,omitempty"`
					Runtime   string         `json:"runtime,omitempty"`
					Model     agentapi.Model `json:"model,omitempty"`
				}
				err = decodeArgs(r.Params.Arguments, &in)
				if err == nil {
					if (name == "send_message" && in.ParentRef != "") || (name == "new_task" && in.TaskRef != "") {
						err = fmt.Errorf("工具参数不正确")
					} else {
						action := "message"
						if name == "new_task" {
							action = "start"
						}
						controller := resolve(who.device)
						if controller == nil {
							err = fmt.Errorf("未知设备")
						} else {
							value, err = controller.Submit(session.Command{Action: action, Prompt: in.Text, TaskRef: in.TaskRef, ParentRef: in.ParentRef, Runtime: in.Runtime, Model: in.Model, Source: "mcp", ConversationID: who.conversation})
						}
					}
				}
			}
			slog.Info("MCP tool call", "tool", name, "device_id", who.device, "linx_session_id", who.conversation, "linx_agent_id", who.agent, "is_error", err != nil, "duration_ms", time.Since(started).Milliseconds())
			if err != nil {
				return errorResult(err), nil
			}
			raw, err := json.Marshal(withDisplayURL(value, publicURL))
			if err != nil {
				return textResult("无法编码工具结果", true), nil
			}
			return textResult(string(raw), false), nil
		})
	}
	getServer := func(*http.Request) *mcp.Server { return s }
	legacy := mcp.NewSSEHandler(getServer, &mcp.SSEOptions{MaxRequestBodyBytes: 32 << 10})
	streamable := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 32 << 10,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accel-Buffering", "no")
		// A bare GET retains legacy SSE. Streamable clients POST to /mcp and
		// identify optional GET streams with protocol/session headers.
		useLegacy := r.URL.Path == "/mcp/message" || (r.Method == http.MethodGet &&
			r.Header.Get("Mcp-Protocol-Version") == "" && r.Header.Get("Mcp-Session-Id") == "")
		if r.Method == http.MethodGet && useLegacy {
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
		if useLegacy {
			legacy.ServeHTTP(w, r)
		} else {
			streamable.ServeHTTP(w, r)
		}
	})
}

// These transport result wrappers keep the domain model independent from the
// public hostname while giving the Linxi agent an exact URL to pass to the
// independent device MCP. Embedding preserves the existing JSON shape.
type receiptResult struct {
	session.Receipt
	DisplayURL string `json:"display_url,omitempty"`
}

type detailResult struct {
	session.Detail
	DisplayURL string `json:"display_url,omitempty"`
}

func withDisplayURL(value any, publicURL string) any {
	switch v := value.(type) {
	case session.Receipt:
		return receiptResult{Receipt: v, DisplayURL: displayURL(publicURL, v.TaskRef)}
	case session.Detail:
		return detailResult{Detail: v, DisplayURL: displayURL(publicURL, v.Ref)}
	default:
		return value
	}
}

func displayURL(publicURL, taskRef string) string {
	if strings.TrimSpace(publicURL) == "" || strings.TrimSpace(taskRef) == "" {
		return ""
	}
	page, err := url.Parse(strings.TrimRight(publicURL, "/") + "/display/")
	if err != nil {
		return ""
	}
	query := page.Query()
	query.Set("task_ref", taskRef)
	// ngrok's free browser interstitial honors this query parameter. The
	// simulated device also uses a non-standard User-Agent because Chromium
	// may follow redirects or request assets without preserving URL hints.
	if isNgrokHost(page.Hostname()) {
		query.Set("ngrok-skip-browser-warning", "true")
	}
	page.RawQuery = query.Encode()
	return page.String()
}

func isNgrokHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasSuffix(host, ".ngrok-free.dev") || strings.HasSuffix(host, ".ngrok.app") || strings.HasSuffix(host, ".ngrok.io")
}

func requestIdentity(r *mcp.CallToolRequest) identity {
	meta, _ := r.Params.Meta[identityMeta].(map[string]any)
	read := func(key string) string { v, _ := meta[key].(string); return v }
	return identity{read("device"), read("conversation"), read("agent")}
}
