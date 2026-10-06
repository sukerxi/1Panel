//go:build !linux

package cmd

import "os/exec"

// The panel only ships on Linux; these are no-op stubs so the package also
// type-checks on other platforms (e.g. in a Windows IDE).
func setProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(_ int) error { return nil }
