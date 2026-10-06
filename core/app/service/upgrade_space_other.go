//go:build !linux

package service

// checkUpgradeSpace is only enforced on Linux, where the panel actually runs.
// The statfs-based implementation lives in upgrade_space_linux.go.
func checkUpgradeSpace() error {
	return nil
}
