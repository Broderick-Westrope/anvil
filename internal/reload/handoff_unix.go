//go:build !windows

package reload

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var getuid = os.Getuid

func checkOwner(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine owner")
	}
	if uid := getuid(); int(st.Uid) != uid {
		return fmt.Errorf("owned by uid %d, not %d", st.Uid, uid)
	}
	return nil
}
