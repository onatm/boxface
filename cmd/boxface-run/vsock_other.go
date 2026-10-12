//go:build !linux

package main

import "errors"

func dialBroker() (brokerConn, error) {
	return nil, errors.New("vsock is only available on Linux")
}
