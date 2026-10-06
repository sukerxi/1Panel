package service

import (
	"fmt"
	"syscall"

	"github.com/1Panel-dev/1Panel/core/global"
)

const minUpgradeFreeSpace = 500 << 20 // 500MB

func checkUpgradeSpace() error {
	dir := global.CONF.Base.InstallDir
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return err
	}
	avail := stat.Bavail * uint64(stat.Bsize)
	if avail < minUpgradeFreeSpace {
		return fmt.Errorf("available space of %s is %d MB, less than required 500MB", dir, avail>>20)
	}
	return nil
}
