//go:build !linux

package terminal

import "errors"

// LocalCommand is a no-op stub on non-Linux platforms: the local PTY terminal
// relies on creack/pty + ioctl which only exist on Linux. The real
// implementation lives in local_cmd.go.

type LocalCommand struct{}

func NewCommand(_ string) (*LocalCommand, error) {
	return nil, errors.New("local terminal is only supported on Linux")
}

func (lcmd *LocalCommand) Read(_ []byte) (int, error)  { return 0, nil }
func (lcmd *LocalCommand) Write(p []byte) (int, error) { return len(p), nil }
func (lcmd *LocalCommand) Close() error                { return nil }

func (lcmd *LocalCommand) ResizeTerminal(_ int, _ int) error { return nil }

func (lcmd *LocalCommand) Wait(quitChan chan bool) {
	setQuit(quitChan)
}
