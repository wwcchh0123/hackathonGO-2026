package linx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testIdentity = Identity{"02:00:00:00:00:01", "550e8400-e29b-41d4-a716-446655440000"}

func TestActivationAndTokenRefresh(t *testing.T) {
	var fetched, checked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Device-Id") != testIdentity.DeviceID {
			t.Error("incorrect device request")
		}
		if r.URL.Path == "/activate" {
			if checked.Add(1) == 1 {
				w.WriteHeader(202)
			} else {
				fmt.Fprint(w, "success")
			}
			return
		}
		if r.Header.Get("Activation-Version") != "1" || r.Header.Get("Client-Id") != testIdentity.ClientID {
			t.Error("missing activation headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["mac_address"] != testIdentity.DeviceID || body["uuid"] != testIdentity.ClientID {
			t.Error("header/body identity mismatch")
		}
		if fetched.Add(1) == 1 {
			fmt.Fprint(w, `{"activation":{"code":"123456"},"websocket":{"url":"ws://example.invalid/ws"}}`)
		} else {
			fmt.Fprint(w, `{"websocket":{"url":"wss://example.invalid/ws","token":"private-device-token"}}`)
		}
	}))
	defer server.Close()
	g := Gateway{URL: server.URL, Identity: testIdentity, PollInterval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var code string
	c, err := g.Pair(ctx, func(a Activation) { code = a.Code })
	if err != nil {
		t.Fatal(err)
	}
	if code != "123456" || c.Token != "private-device-token" || fetched.Load() != 2 || checked.Load() != 2 {
		t.Fatal("activation lifecycle did not refresh token")
	}
}

func TestOTAErrorsAndCancellation(t *testing.T) {
	for _, body := range []string{`{"error":"private-data"}`, `{"code":500,"msg":"private-data"}`, `{}`, `bad-json`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			g := Gateway{URL: s.URL, Identity: testIdentity}
			if _, err := g.Fetch(context.Background()); err == nil || strings.Contains(err.Error(), "private-data") {
				t.Fatalf("unexpected fetch error: %v", err)
			}
			if yes, err := g.Activated(context.Background()); err == nil || yes {
				t.Fatal("non-success 200 must not bind a device")
			}
		})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"activation":{"code":"123456"}}`) }))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	g := Gateway{URL: s.URL, Identity: testIdentity}
	if _, err := g.Pair(ctx, func(Activation) { cancel() }); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}
