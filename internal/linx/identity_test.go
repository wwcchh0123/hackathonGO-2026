package linx

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestIdentityPersistentAndPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadIdentity(dir)
	if err != nil || first != second {
		t.Fatalf("identity changed: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions: %v", info.Mode())
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir); err == nil {
		t.Fatal("corrupt identity must not be silently replaced")
	}
}

func TestConcurrentIdentityCreation(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan Identity, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			id, err := LoadIdentity(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	var first Identity
	for id := range ids {
		if first.DeviceID == "" {
			first = id
		}
		if first != id {
			t.Fatal("concurrent startup created different devices")
		}
	}
}
