//go:build !linux && !darwin

package ui

import "golang.org/x/term"

func SetNonCanonical(fd int) (func(), error) {
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() {
		_ = term.Restore(fd, oldState)
	}, nil
}
