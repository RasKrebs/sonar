//go:build !linux

package ports

// nativeParentTable has no kernel-provided process table to read outside
// Linux; ParentTable falls back to `ps`.
func nativeParentTable() map[int]int { return nil }
