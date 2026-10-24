// Package mcp exposes a small voice vocabulary through the official MCP SDK.
package mcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func Handler(c *session.Controller) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "linxi-workstation", Version: "0.1.0"}, nil)
	type prompt struct {
		Prompt string `json:"prompt" jsonschema:"用户要实现的需求"`
	}
	type interrupt struct {
		NewInstruction string `json:"new_instruction,omitempty" jsonschema:"修改需求，可省略以仅暂停"`
	}
	type empty struct{}
	type view struct {
		Target device.View `json:"target" jsonschema:"timeline、code、terminal 或 preview"`
	}
	result := func(r session.Receipt, e error) (*mcp.CallToolResult, any, error) {
		if e != nil {
			return nil, nil, e
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: r.Message}}}, r, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "start_task", Description: "开始新编程任务；立即返回，后台创建云端会话"}, func(ctx context.Context, r *mcp.CallToolRequest, in prompt) (*mcp.CallToolResult, any, error) {
		receipt, err := c.Submit(session.Command{Action: "start", Prompt: in.Prompt})
		return result(receipt, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "interrupt", Description: "暂停当前任务；如有修改需求，等待停止后在原会话发送"}, func(ctx context.Context, r *mcp.CallToolRequest, in interrupt) (*mcp.CallToolResult, any, error) {
		receipt, err := c.Submit(session.Command{Action: "interrupt", Prompt: in.NewInstruction})
		return result(receipt, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "continue_task", Description: "在原会话继续执行"}, func(ctx context.Context, r *mcp.CallToolRequest, in empty) (*mcp.CallToolResult, any, error) {
		receipt, err := c.Submit(session.Command{Action: "continue"})
		return result(receipt, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_status", Description: "用一句话报告当前进度"}, func(ctx context.Context, r *mcp.CallToolRequest, in empty) (*mcp.CallToolResult, any, error) {
		snapshot := c.State.Get()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: snapshot.Summary}}}, snapshot, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "show", Description: "切换设备展示画面；看看效果时用 preview"}, func(ctx context.Context, r *mcp.CallToolRequest, in view) (*mcp.CallToolResult, any, error) {
		receipt, err := c.Submit(session.Command{Action: "show", View: in.Target})
		return result(receipt, err)
	})
	h := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server { return s }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK advertises the GET path as its POST endpoint. Rewrite the
		// cloned URL to satisfy Linx's /mcp + /mcp/message convention.
		if r.Method == http.MethodGet {
			r = r.Clone(r.Context())
			u := *r.URL
			u.Path = "/mcp/message"
			r.URL = &u
		}
		h.ServeHTTP(w, r)
	})
}
