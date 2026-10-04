package service

import (
	"sync"

	"github.com/1Panel-dev/1Panel/core/app/dto"
	"github.com/1Panel-dev/1Panel/core/utils/files"
)

// Upgrade stages reported to the frontend.
const (
	StagePreparing  = "preparing"
	StageDownload   = "download"
	StageDecompress = "decompress"
	StageBackup     = "backup"
	StageInstall    = "install"
	StageRestart    = "restart"
)

type upgradeProgressStore struct {
	sync.Mutex
	data dto.UpgradeProgress
}

var upgradeProgress = &upgradeProgressStore{}

func (p *upgradeProgressStore) Start(version string, mirrors []files.DownloadCandidate) {
	p.Lock()
	defer p.Unlock()
	p.data = dto.UpgradeProgress{
		Running: true,
		Failed:  false,
		Stage:   StagePreparing,
		Version: version,
		Mirrors: make([]dto.UpgradeMirror, 0, len(mirrors)),
	}
	for _, item := range mirrors {
		p.data.Mirrors = append(p.data.Mirrors, dto.UpgradeMirror{Name: item.Name, Status: "waiting"})
	}
}

func (p *upgradeProgressStore) SetStage(stage string) {
	p.Lock()
	defer p.Unlock()
	p.data.Stage = stage
}

func (p *upgradeProgressStore) SetDownload(downloaded, total, speedBps int64) {
	p.Lock()
	defer p.Unlock()
	p.data.Stage = StageDownload
	p.data.Downloaded = downloaded
	p.data.Total = total
	p.data.SpeedBps = speedBps
}

func (p *upgradeProgressStore) SetMirror(name, status, detail string) {
	p.Lock()
	defer p.Unlock()
	for i := range p.data.Mirrors {
		if p.data.Mirrors[i].Name == name {
			p.data.Mirrors[i].Status = status
			p.data.Mirrors[i].Detail = detail
			return
		}
	}
}

func (p *upgradeProgressStore) Fail(message string) {
	p.Lock()
	defer p.Unlock()
	p.data.Running = false
	p.data.Failed = true
	p.data.Message = message
}

func (p *upgradeProgressStore) IsRunning() bool {
	p.Lock()
	defer p.Unlock()
	return p.data.Running
}

// LoadUpgradeProgress returns a copy safe to serialize.
func LoadUpgradeProgress() dto.UpgradeProgress {
	upgradeProgress.Lock()
	defer upgradeProgress.Unlock()
	snapshot := upgradeProgress.data
	if snapshot.Mirrors != nil {
		snapshot.Mirrors = append([]dto.UpgradeMirror(nil), snapshot.Mirrors...)
	}
	return snapshot
}
