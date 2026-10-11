//go:build !linux

package mcp

import (
	"errors"
	"net"
)

// peerCredentials не реализован вне Linux: без PID собеседника мост не может
// понять, к какой задаче относится вызов, и отклоняет его.
func peerCredentials(*net.UnixConn) (pid, uid int, err error) {
	return 0, 0, errors.New("peer credentials are supported only on Linux")
}
