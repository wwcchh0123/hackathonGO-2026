package devicedisplay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerServesEmbeddedDisplayAndRejectsUnknownAssets(t *testing.T) {
	server := httptest.NewServer(StaticHandler())
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "session-list") {
		t.Fatalf("display index: status=%d body=%q", response.StatusCode, body)
	}
	missing, err := server.Client().Get(server.URL + "/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status: %d", missing.StatusCode)
	}
}
