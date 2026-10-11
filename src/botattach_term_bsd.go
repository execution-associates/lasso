//go:build darwin || freebsd || netbsd || openbsd

package main

import "golang.org/x/sys/unix"

// See botattach_term_linux.go.
const (
	attachGetTermios = unix.TIOCGETA
	attachSetTermios = unix.TIOCSETA
)
