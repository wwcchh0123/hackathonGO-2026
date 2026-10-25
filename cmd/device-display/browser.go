package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecontrol"
)

type devtoolsTarget struct {
	ID                   string `json:"id"`
	ParentID             string `json:"parentId"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type devtoolsResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

const (
	// A 1920x1080 PNG is often larger than the websocket package's 32 KiB
	// default message limit. Keep the CDP response bounded while allowing the
	// screenshot limit below to arrive as base64 JSON.
	maxBrowserImageBytes = 8 << 20
	cdpReadLimit         = 12 << 20
)

// Capture takes the same Chromium surface that the AirPlay sender mirrors.
// Keeping this on the simulated device is intentional: the cloud can inspect
// the device browser without receiving the browser's debugging endpoint.
func (b *browser) Capture(ctx context.Context) (devicecontrol.BrowserCapture, error) {
	b.actionMu.Lock()
	defer b.actionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	target, err := b.page(ctx)
	if err != nil {
		return devicecontrol.BrowserCapture{}, err
	}
	var result struct {
		Data string `json:"data"`
	}
	if err := b.command(ctx, target, 1, "Page.captureScreenshot", map[string]any{
		"format":      "png",
		"fromSurface": true,
	}, &result); err != nil {
		return devicecontrol.BrowserCapture{}, fmt.Errorf("获取浏览器截图: %w", err)
	}
	png, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(png) == 0 {
		return devicecontrol.BrowserCapture{}, fmt.Errorf("浏览器返回了无效截图")
	}
	if len(png) > maxBrowserImageBytes {
		return devicecontrol.BrowserCapture{}, fmt.Errorf("浏览器截图过大")
	}
	capture := devicecontrol.BrowserCapture{URL: target.URL, Title: target.Title, PNG: png}
	inspection, err := b.inspect(ctx)
	if err != nil {
		capture.StructureError = err.Error()
	} else {
		capture.Inspection = &inspection
	}
	return capture, nil
}

//go:embed browser_dom.js
var browserDOMScript string

func domExpression(args map[string]any) string {
	raw, _ := json.Marshal(args)
	return "(" + browserDOMScript + ")(" + string(raw) + ")"
}

// Inspect runs a fixed, read-only DOM projection inside Chromium. It exposes
// visible HTML structure and text without forwarding a general-purpose JavaScript
// or selector API to the cloud agent.
func (b *browser) Inspect(ctx context.Context) (devicecontrol.BrowserInspection, error) {
	b.actionMu.Lock()
	defer b.actionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return b.inspect(ctx)
}

func (b *browser) inspect(ctx context.Context) (devicecontrol.BrowserInspection, error) {
	// Clear old bindings even if discovery fails, so stale IDs cannot point at
	// controls in another document. A monotonically increasing counter also
	// prevents a later snapshot from silently reassigning an old element ID.
	b.elements = nil
	page, err := b.page(ctx)
	if err != nil {
		return devicecontrol.BrowserInspection{}, err
	}
	targets, err := b.targets(ctx)
	if err != nil {
		return devicecontrol.BrowserInspection{}, err
	}
	selected := []devtoolsTarget{page}
	seen := map[string]bool{page.ID: true}
	for i := 0; i < len(selected) && len(selected) < 8; i++ {
		for _, target := range targets {
			if target.Type == "iframe" && target.ParentID == selected[i].ID && !seen[target.ID] && target.WebSocketDebuggerURL != "" {
				seen[target.ID] = true
				selected = append(selected, target)
				if len(selected) == 8 {
					break
				}
			}
		}
	}
	combined := devicecontrol.BrowserInspection{URL: page.URL, Title: page.Title, Elements: []devicecontrol.BrowserElement{}, Partial: len(selected) == 8}
	elementTargets := map[string]devtoolsTarget{}
	for i, target := range selected {
		var result struct {
			Result struct {
				Value devicecontrol.BrowserInspection `json:"value"`
			} `json:"result"`
			Exception json.RawMessage `json:"exceptionDetails"`
		}
		err := b.command(ctx, target, 1, "Runtime.evaluate", map[string]any{
			"expression": domExpression(map[string]any{"action": "inspect", "start": b.nextElement, "limit": 240}), "returnByValue": true,
		}, &result)
		if err != nil || len(result.Exception) > 0 || result.Result.Value.URL == "" {
			if i == 0 {
				return devicecontrol.BrowserInspection{}, fmt.Errorf("读取浏览器页面失败，请稍后重新查看")
			}
			combined.Partial = true
			continue
		}
		inspection := result.Result.Value
		if i == 0 {
			combined.VisibleText = inspection.VisibleText
		}
		combined.Partial = combined.Partial || inspection.Partial
		combined.Frames = append(combined.Frames, inspection.Frames...)
		combined.Elements = append(combined.Elements, inspection.Elements...)
		for _, element := range inspection.Elements {
			elementTargets[element.ID] = target
		}
		b.nextElement += uint64(len(inspection.Elements))
	}
	b.elements = elementTargets
	return combined, nil
}

// Click resolves the short-lived ID created by Inspect, scrolls that element
// into view, then sends a real mouse press/release pair through CDP. Resolving
// the element again prevents an old coordinate from being applied after a
// card expansion or page reflow.
func (b *browser) Click(ctx context.Context, elementID string) error {
	b.actionMu.Lock()
	defer b.actionMu.Unlock()
	if !validElementID(elementID) {
		return fmt.Errorf("元素 ID 无效")
	}
	target, ok := b.elements[elementID]
	if !ok {
		return fmt.Errorf("页面元素已失效，请重新查看页面")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var result struct {
		Result struct {
			Value struct {
				Found bool    `json:"found"`
				Error string  `json:"error"`
				X     float64 `json:"x"`
				Y     float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := b.command(ctx, target, 1, "Runtime.evaluate", map[string]any{
		"expression": domExpression(map[string]any{"action": "click", "elementID": elementID}), "returnByValue": true,
	}, &result); err != nil {
		return fmt.Errorf("定位页面元素: %w", err)
	}
	point := result.Result.Value
	if len(result.Exception) > 0 || !point.Found {
		return fmt.Errorf("页面元素已失效，请重新查看页面")
	}
	if point.Error != "" {
		return fmt.Errorf("无法点击: %s", point.Error)
	}
	if point.X < 0 || point.Y < 0 || point.X > float64(b.width) || point.Y > float64(b.height) {
		return fmt.Errorf("元素不在可点击区域")
	}
	for i, kind := range []string{"mousePressed", "mouseReleased"} {
		if err := b.command(ctx, target, i+2, "Input.dispatchMouseEvent", map[string]any{
			"type": kind, "x": point.X, "y": point.Y, "button": "left", "clickCount": 1,
		}, nil); err != nil {
			return fmt.Errorf("点击页面元素: %w", err)
		}
	}
	return nil
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

func (b *browser) page(ctx context.Context) (devtoolsTarget, error) {
	if b.debugPort <= 0 {
		return devtoolsTarget{}, fmt.Errorf("浏览器调试端口未配置")
	}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		targets, err := b.targets(ctx)
		if err != nil {
			lastErr = err
		} else {
			for _, target := range targets {
				if target.Type == "page" && target.WebSocketDebuggerURL != "" {
					return target, nil
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return devtoolsTarget{}, fmt.Errorf("连接浏览器调试接口: %w", lastErr)
			}
			return devtoolsTarget{}, fmt.Errorf("没有找到投屏页面")
		}
		select {
		case <-ctx.Done():
			return devtoolsTarget{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// command sends one bounded CDP request over a fresh page connection. A fresh
// connection keeps requests isolated and lets the browser discard a stale
// page target naturally after navigation.
func (b *browser) command(ctx context.Context, target devtoolsTarget, id int, method string, params any, out any) error {
	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "command complete")
	conn.SetReadLimit(cdpReadLimit)
	body, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			return err
		}
		var response devtoolsResponse
		if err := json.Unmarshal(data, &response); err != nil || response.ID != id {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return fmt.Errorf("CDP %s: %s", method, response.Error)
		}
		if out != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func (b *browser) targets(ctx context.Context) ([]devtoolsTarget, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", b.debugPort), nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("浏览器调试接口返回 HTTP %d", response.StatusCode)
	}
	var targets []devtoolsTarget
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}
