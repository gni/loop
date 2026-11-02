//go:build linux

package ui

import (
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func SetNonCanonical(fd int) (func(), error) {
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	oldTermios := *termios

	termios.Lflag &^= unix.ICANON | unix.ECHO
	// Preserve CR for Enter and LF for Ctrl+J while a reply streams.
	termios.Iflag &^= unix.IXON | unix.IXOFF | unix.ICRNL | unix.INLCR | unix.IGNCR
	// Ctrl+Z undoes a queued prompt instead of suspending Loop mid-turn.
	termios.Cc[unix.VSUSP] = 0
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		return nil, err
	}

	return func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &oldTermios)
	}, nil
}
