//go:build !linux

package daemon

// reloading is the systemd notification at the start of a reload. Only Linux runs systemd.
func reloading() string { return "RELOADING=1" }
