//go:build !linux

package mounter

import "fmt"

type unsupportedMounter struct{}

func New() (Interface, error) {
	return &unsupportedMounter{}, nil
}

func (m *unsupportedMounter) BindMount(source, target string, readonly bool) error {
	return fmt.Errorf("bind mount is only supported on linux")
}

func (m *unsupportedMounter) Unmount(target string) error {
	return fmt.Errorf("unmount is only supported on linux")
}

func (m *unsupportedMounter) IsMountPoint(path string) (bool, error) {
	return false, fmt.Errorf("mountpoint checks are only supported on linux")
}
