//go:build linux

package selfupdate

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func exchangeExecutable(executable, staged string) (string, error) {
	if err := unix.Renameat2(unix.AT_FDCWD, executable, unix.AT_FDCWD, staged, unix.RENAME_EXCHANGE); err != nil {
		return "", fmt.Errorf("atomically exchange updated binary: %w", err)
	}
	return staged, nil
}
