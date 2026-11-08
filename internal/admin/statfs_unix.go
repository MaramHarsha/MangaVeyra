//go:build linux || darwin || freebsd

package admin

import "syscall"

// diskSize returns the size in bytes of the file system holding path (0 when unknown).
func diskSize(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(uint64(st.Bsize) * st.Blocks) //nolint:gosec // block counts of real file systems fit in int64
}
