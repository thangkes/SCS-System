//go:build !linux

package probe

import (
	"context"
	"net/netip"
	"time"
)

func ping(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
	return 0, ErrUnsupported
}

func defaultGateway() (netip.Addr, error) { return netip.Addr{}, ErrUnsupported }
