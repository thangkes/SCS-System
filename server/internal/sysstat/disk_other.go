//go:build !(linux || darwin || freebsd)

package sysstat

func diskUsage(string) (string, uint64, uint64) { return "", 0, 0 }
