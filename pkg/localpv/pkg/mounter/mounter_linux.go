//go:build linux

package mounter

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxMounter struct{}

func New() (Interface, error) {
	return &linuxMounter{}, nil
}

func (m *linuxMounter) BindMount(source, target string, readonly bool) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("mount bind %q -> %q: %w", source, target, err)
	}
	if readonly {
		if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("remount readonly %q: %w", target, err)
		}
	}
	return nil
}

func (m *linuxMounter) Unmount(target string) error {
	if err := unix.Unmount(target, 0); err != nil {
		return fmt.Errorf("unmount %q: %w", target, err)
	}
	return nil
}

func (m *linuxMounter) IsMountPoint(path string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// mount point is the 5th whitespace-separated field.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[4] == path {
			return true, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return false, nil
}
