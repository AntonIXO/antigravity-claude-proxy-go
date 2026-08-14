//go:build windows

package auth

func flockShared(fd uintptr) error {
	return nil
}

func flockExclusive(fd uintptr) error {
	return nil
}

func flockUnlock(fd uintptr) error {
	return nil
}
