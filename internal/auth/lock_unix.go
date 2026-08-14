//go:build !windows

package auth

import (
	"golang.org/x/sys/unix"
)

func flockShared(fd uintptr) error {
	return unix.Flock(int(fd), unix.LOCK_SH)
}

func flockExclusive(fd uintptr) error {
	return unix.Flock(int(fd), unix.LOCK_EX)
}

func flockUnlock(fd uintptr) error {
	return unix.Flock(int(fd), unix.LOCK_UN)
}
