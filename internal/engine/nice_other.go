//go:build !linux

package engine

// withNice is a no-op off Linux: per-thread niceness (and pool-inheritance of it)
// is a Linux behavior. fn runs at the default priority.
func withNice(nice int, fn func()) { fn() }
