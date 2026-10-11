package main

import "golang.org/x/sys/unix"

// The terminal ioctls lasso bot-attach reads and sets raw mode with: Linux
// names them TCGETS/TCSETS, the BSDs (macOS) TIOCGETA/TIOCSETA.
const (
	attachGetTermios = unix.TCGETS
	attachSetTermios = unix.TCSETS
)
