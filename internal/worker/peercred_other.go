//go:build !linux

package worker

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer credentials are only supported on linux")
}
