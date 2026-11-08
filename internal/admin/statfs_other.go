//go:build !(linux || darwin || freebsd)

package admin

// diskSize is unknown on this platform; the admin panel shows usage only.
func diskSize(string) int64 { return 0 }
