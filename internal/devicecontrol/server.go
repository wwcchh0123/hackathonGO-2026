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

// BrowserCapture is the current browser frame. The image is returned as MCP
// image content so the Linxi agent can inspect exactly what is being mirrored
// over AirPlay instead of reasoning from the task event stream alone.
type BrowserCapture struct {
	URL            string
	Title          string
	PNG            []byte
	Inspection     *BrowserInspection
	StructureError string
}

// BrowserInspection contains a bounded projection of the rendered DOM, not a
// preselected list of controls. ElementID values expire on the next capture or
// inspection, or when their document navigates.
type BrowserInspection struct {
	URL         string           `json:"url"`
	Title       string           `json:"title"`
	VisibleText string           `json:"visible_text,omitempty"`
	Elements    []BrowserElement `json:"elements"`
	Frames      []BrowserFrame   `json:"frames,omitempty"`
	Partial     bool             `json:"partial,omitempty"`
}

// Frames identify embedded preview documents so the agent can distinguish
// task history from the actual running app, even without image support.
type BrowserFrame struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	VisibleText string `json:"visible_text,omitempty"`
	HTML        string `json:"html"`
}

type BrowserElement struct {
	ID       string        `json:"element_id"`
	Tag      string        `json:"tag"`
	Role     string        `json:"role,omitempty"`
	Label    string        `json:"label,omitempty"`
	Text     string        `json:"text,omitempty"`
	FrameURL string        `json:"frame_url,omitempty"`
	Expanded *bool         `json:"expanded,omitempty"`
	Disabled bool          `json:"disabled,omitempty"`
	Bounds   BrowserBounds `json:"bounds"`
}

type BrowserBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Runtime interface {
	ScanScreens(context.Context) ([]Screen, error)
	CastScreen(context.Context, string) error
	ScrollScreen(context.Context, string, int) error
	CaptureScreen(context.Context) (BrowserCapture, error)
	InspectBrowser(context.Context) (BrowserInspection, error)
	ClickBrowser(context.Context, string) error
	StopCast(context.Context) error
}

// Handler returns the legacy SSE transport used by the Linxi cloud. The
// reverse proxy can mount this handler under /device/ while the control MCP
// remains mounted under /mcp on the same public ngrok endpoint.
func Handler(runtime Runtime) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "linxi-device-display", Version: "0.3.0"}, nil)
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
		Name:        "scroll_screen",
		Description: "滚动当前投屏网页。direction 使用 up 或 down；pages 表示滚动页数，默认 1。只改变展示位置，不修改 AgentAPI 任务。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"direction": map[string]any{"type": "string", "enum": []string{"up", "down"}, "description": "滚动方向"},
				"pages":     map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "滚动页数，默认 1"},
			},
			"required":             []string{"direction"},
			"additionalProperties": false,
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Direction string `json:"direction"`
			Pages     int    `json:"pages,omitempty"`
		}
		if err := decode(request.Params.Arguments, &input); err != nil || (input.Direction != "up" && input.Direction != "down") {
			return textResult("滚动方向无效，需要 up 或 down", true), nil
		}
		if input.Pages == 0 {
			input.Pages = 1
		}
		if input.Pages < 1 || input.Pages > 5 {
			return textResult("滚动页数无效，需要 1 到 5", true), nil
		}
		if err := runtime.ScrollScreen(ctx, input.Direction, input.Pages); err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult("已滚动当前投屏页面", false), nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "capture_screen",
		Description: "一次返回当前投屏截图和实际 DOM/HTML 结构，包括 iframe 作品内的元素属性、层级及 element_id。结合图片与结构定位后直接 click_browser；结构读取不完整会标记 partial，不能据此断言预览只读。",
		InputSchema: emptySchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capture, err := runtime.CaptureScreen(ctx)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		if len(capture.PNG) == 0 {
			return textResult("当前没有可用的浏览器画面", true), nil
		}
		metadata, _ := json.Marshal(struct {
			URL            string             `json:"url"`
			Title          string             `json:"title"`
			Page           *BrowserInspection `json:"page,omitempty"`
			StructureError string             `json:"structure_error,omitempty"`
		}{capture.URL, capture.Title, capture.Inspection, capture.StructureError})
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: string(metadata)},
			&mcp.ImageContent{Data: capture.PNG, MIMEType: "image/png"},
		}}, nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "inspect_browser",
		Description: "读取投屏主页面及 iframe 内的实际 DOM/HTML 树，不限 button：div、卡片、SVG、canvas 等可见节点均有 element_id。结合 frames[].html 的层级和属性与截图判断交互目标，再点击对应 ID。frame_url 标明文档；不是所有节点都可交互。",
		InputSchema: emptySchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		inspection, err := runtime.InspectBrowser(ctx)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		data, err := json.Marshal(inspection)
		if err != nil {
			return textResult("无法编码页面检查结果", true), nil
		}
		return textResult(string(data), false), nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "click_browser",
		Description: "点击最近一次 capture_screen 或 inspect_browser 返回的 element_id，支持 iframe 内外的真实 DOM 元素，包括 div、卡片、链接和 SVG。点击后检查变化，再简短告知结果；无需重新投屏。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"element_id": map[string]any{"type": "string", "pattern": `^e[0-9]{1,12}$`, "description": "最近一次 capture_screen 或 inspect_browser 返回的元素 ID"},
			},
			"required":             []string{"element_id"},
			"additionalProperties": false,
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			ElementID string `json:"element_id"`
		}
		if err := decode(request.Params.Arguments, &input); err != nil || !validElementID(input.ElementID) {
			return textResult("元素 ID 无效，请先调用 inspect_browser，再使用其中的 element_id", true), nil
		}
		if err := runtime.ClickBrowser(ctx, input.ElementID); err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult("已点击页面元素，请重新查看浏览器画面确认结果", false), nil
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

func validElementID(raw string) bool {
	if len(raw) < 2 || len(raw) > 13 || raw[0] != 'e' {
		return false
	}
	for _, r := range raw[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func textResult(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}
