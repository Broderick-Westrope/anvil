//go:build windows

package reload

import "os"

// The handoff dir lives under the user's data dir, whose ACLs already limit
// access; Windows has no cheap uid comparison.
func checkOwner(os.FileInfo) error { return nil }
