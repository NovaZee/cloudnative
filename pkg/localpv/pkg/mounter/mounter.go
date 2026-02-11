package mounter

type Interface interface {
	// BindMount bind-mounts source directory to target.
	BindMount(source, target string, readonly bool) error
	Unmount(target string) error
	IsMountPoint(path string) (bool, error)
}
