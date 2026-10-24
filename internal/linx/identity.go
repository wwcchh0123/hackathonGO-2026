// Package linx implements the Lingxi device protocol, independently of AgentAPI.
package linx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
)

type Identity struct {
	DeviceID string `json:"device_id"`
	ClientID string `json:"client_id"`
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// LoadIdentity creates a stable software device. It does not read or modify a NIC.
func LoadIdentity(dir string) (Identity, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Identity{}, err
	}
	path := filepath.Join(dir, "identity.json")
	read := func() (Identity, error) {
		var id Identity
		b, err := os.ReadFile(path)
		if err != nil {
			return id, err
		}
		if err = json.Unmarshal(b, &id); err != nil {
			return id, fmt.Errorf("invalid device identity: %w", err)
		}
		mac, err := net.ParseMAC(id.DeviceID)
		if err != nil || len(mac) != 6 || !uuidV4.MatchString(id.ClientID) {
			return id, fmt.Errorf("invalid device identity in %s; restore the original identity instead of silently changing devices", path)
		}
		return id, nil
	}
	if _, err := os.Stat(path); err == nil {
		return read()
	} else if !os.IsNotExist(err) {
		return Identity{}, err
	}
	var b [22]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Identity{}, err
	}
	b[0] = (b[0] | 2) & 0xfe // Locally administered, unicast pseudo-MAC.
	u := b[6:]
	u[6] = (u[6] & 15) | 64
	u[8] = (u[8] & 63) | 128
	h := hex.EncodeToString(u)
	id := Identity{net.HardwareAddr(b[:6]).String(), h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]}
	data, _ := json.MarshalIndent(id, "", "  ")
	// Publish atomically without replacing an identity another process created.
	f, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return Identity{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return Identity{}, err
	}
	if err = f.Close(); err != nil {
		return Identity{}, err
	}
	if err = os.Link(f.Name(), path); os.IsExist(err) {
		return read()
	} else if err != nil {
		return Identity{}, err
	}
	return id, nil
}
