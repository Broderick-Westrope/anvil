// Package machineid provides a stable machine identifier for use in
// provider headers (e.g. the Hyper provider's x-anvil-id header).
package machineid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sync"

	"github.com/denisbrodbeck/machineid"
)

// hashKey must remain "charm" to preserve ID compatibility with the
// Hyper provider backend.
const hashKey = "charm"

var (
	once sync.Once
	id   string
)

// Get returns a stable, cached machine identifier. It tries
// machineid.ProtectedID first, falls back to a MAC address hash, and
// returns "unknown" if both fail.
func Get() string {
	once.Do(func() {
		if mid, err := machineid.ProtectedID(hashKey); err == nil {
			id = mid
			return
		}
		if macAddr, err := getMacAddr(); err == nil {
			id = hashString(macAddr)
			return
		}
		id = "unknown"
	})
	return id
}

func getMacAddr() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 && len(iface.HardwareAddr) > 0 {
			if addrs, err := iface.Addrs(); err == nil && len(addrs) > 0 {
				return iface.HardwareAddr.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no active interface with mac address found")
}

func hashString(str string) string {
	hash := hmac.New(sha256.New, []byte(str))
	hash.Write([]byte(hashKey))
	return hex.EncodeToString(hash.Sum(nil))
}
