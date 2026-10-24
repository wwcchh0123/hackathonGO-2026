// Package devicecontrol exposes the small MCP surface implemented by the
// Linxi hardware. It deliberately knows nothing about AgentAPI sessions: the
// hardware only opens a page and mirrors its own display over AirPlay.
package devicecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Screen struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Runtime interface {
	ScanScreens(context.Context) ([]Screen, error)
	CastScreen(context.Context, string) error
	StopCast(context.Context) error
}

// Handler returns the legacy SSE transport used by the Linxi cloud. The
// reverse proxy can mount this handler under /device/ while the control MCP
// remains mounted under /mcp on the same public ngrok endpoint.
func Handler(runtime Runtime) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "linxi-device-display", Version: "0.1.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "scan_screens",
		Description: "扫描本地局域网可用的投屏目标，返回 screen_id 和名称。",
		InputSchema: emptySchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		screens, err := runtime.ScanScreens(ctx)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		data, _ := json.Marshal(screens)
		return textResult(string(data), false), nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "cast_screen",
		Description: "让设备浏览器打开指定网页，并把浏览器画面通过 AirPlay 镜像到目标屏幕。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要在设备浏览器打开的完整 http(s) 网页地址"},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			URL string `json:"url"`
		}
		if err := decode(request.Params.Arguments, &input); err != nil || !validURL(input.URL) {
			return textResult("网页地址无效，需要完整的 http 或 https 地址", true), nil
		}
		if err := runtime.CastScreen(ctx, input.URL); err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult("已打开网页并开始投屏", false), nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "stop_cast",
		Description: "停止当前 AirPlay 投屏，但不停止云端 AgentAPI 编程任务。",
		InputSchema: emptySchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := runtime.StopCast(ctx); err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult("已停止投屏，云端任务仍会继续", false), nil
	})

	getServer := func(*http.Request) *mcp.Server { return server }
	sse := mcp.NewSSEHandler(getServer, &mcp.SSEOptions{MaxRequestBodyBytes: 32 << 10})
	streamable := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 32 << 10,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accel-Buffering", "no")
		// Keep the old SSE path for existing Linxi device clients. New custom
		// MCP entries use Streamable HTTP at the same /mcp URL.
		useLegacy := r.URL.Path == "/mcp/message" || (r.Method == http.MethodGet &&
			r.Header.Get("Mcp-Protocol-Version") == "" && r.Header.Get("Mcp-Session-Id") == "")
		if r.Method == http.MethodGet && useLegacy {
			r = r.Clone(r.Context())
			u := *r.URL
			// The SSE SDK emits a relative POST endpoint. When nginx mounts this
			// service below /device, preserve that public prefix or the Linxi cloud
			// would post the follow-up call to the control service's /mcp/message.
			prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
			u.Path = prefix + "/mcp/message"
			r.URL = &u
		} else if r.Method == http.MethodPost && useLegacy {
			// The SDK's SSE transport reads POST JSON from the request body. Bound
			// it here so a public device endpoint cannot accumulate an unbounded
			// request before MCP validates the tool arguments.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
			if err != nil {
				http.Error(w, "请求过大或无法读取", http.StatusRequestEntityTooLarge)
				return
			}
			r = r.Clone(r.Context())
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		if useLegacy {
			sse.ServeHTTP(w, r)
			return
		}
		streamable.ServeHTTP(w, r)
	})
}

func emptySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func decode(arguments json.RawMessage, out any) error {
	if len(arguments) == 0 {
		arguments = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func validURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func textResult(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}
