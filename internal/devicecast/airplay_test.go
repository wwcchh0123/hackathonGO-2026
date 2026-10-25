package devicecast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("ISSUE26_SENDER_HELPER") == "1" {
		log, err := os.OpenFile(os.Getenv("ISSUE26_CAST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = log.WriteString("start\n")
		_ = log.Close()
		switch os.Getenv("ISSUE26_SENDER_MODE") {
		case "block":
			time.Sleep(time.Hour)
		case "fail":
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeSender(t *testing.T, mode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	log := filepath.Join(root, "starts")
	t.Setenv("ISSUE26_CAST_LOG", log)
	t.Setenv("ISSUE26_SENDER_HELPER", "1")
	t.Setenv("ISSUE26_SENDER_MODE", mode)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return binary, log
}

func waitForStarts(t *testing.T, log string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		if strings.Count(string(data), "start\n") >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sender did not start %d times", count)
}

func TestSenderStopsActiveProcessOnCancellation(t *testing.T) {
	binary, log := fakeSender(t, "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentials := filepath.Join(t.TempDir(), "pairing", "credentials.json")
	done := make(chan error, 1)
	go func() {
		done <- New(Config{Binary: binary, Target: "127.0.0.1", Port: 7001, Credentials: credentials, Password: "test-pin"}).Run(ctx)
	}()
	waitForStarts(t, log, 1, 2*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not terminate the active sender")
	}
	if info, err := os.Stat(filepath.Dir(credentials)); err != nil || !info.IsDir() {
		t.Fatal("pairing directory unavailable", err)
	}
}

func TestSenderRetriesExitedProcessAndCancelsBackoff(t *testing.T) {
	binary, log := fakeSender(t, "fail")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- New(Config{Binary: binary, Target: "127.0.0.1"}).Run(ctx) }()
	waitForStarts(t, log, 2, 6*time.Second)
	// The second process exits immediately; stop must also cancel its retry.
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel sender backoff")
	}
	data, _ := os.ReadFile(log)
	if strings.Count(string(data), "start\n") != 2 {
		t.Fatal("unexpected sender duplication", string(data))
	}
}

func TestSenderConfigurationErrorsAreExplicit(t *testing.T) {
	if err := New(Config{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := New(Config{Target: "127.0.0.1"}).Run(context.Background()); err == nil {
		t.Fatal("missing sender was accepted")
	}
	binary, _ := fakeSender(t, "exit")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(Config{Binary: binary, Credentials: filepath.Join(file, "credentials.json")}).runOnce(context.Background()); err == nil {
		t.Fatal("invalid pairing directory was accepted")
	}
}
