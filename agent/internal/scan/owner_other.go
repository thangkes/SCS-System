//go:build !unix

package scan

func sameOwner(string, ...string) error { return nil }
