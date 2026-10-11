//go:build linux

package mcp

import (
	"net"
	"syscall"
)

// peerCredentials возвращает PID и UID процесса на той стороне сокета. Значения
// даёт ядро (SO_PEERCRED), собеседник подменить их не может.
func peerCredentials(conn *net.UnixConn) (pid, uid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if credErr != nil {
		return 0, 0, credErr
	}
	return int(cred.Pid), int(cred.Uid), nil
}
