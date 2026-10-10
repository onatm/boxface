package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func dialBroker() (brokerConn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: hostCID, Port: brokerPort}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}
