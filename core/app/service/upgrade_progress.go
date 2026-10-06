package service

import (
	"fmt"
	"sync"
	"time"

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

// Log levels rendered by the frontend log console.
const (
	logLevelInfo    = "info"
	logLevelSuccess = "success"
	logLevelWarn    = "warn"
	logLevelError   = "error"
)

// maxUpgradeLogLines keeps the in-memory ring buffer bounded for long downloads.
const maxUpgradeLogLines = 300

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
		Logs:    make([]dto.UpgradeLogLine, 0, 32),
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

// appendLine must be called with the lock held.
func (p *upgradeProgressStore) appendLine(level, message string) {
	line := dto.UpgradeLogLine{
		Time:    time.Now().Format("2006-01-02 15:04:05"),
		Level:   level,
		Message: message,
	}
	p.data.Logs = append(p.data.Logs, line)
	if len(p.data.Logs) > maxUpgradeLogLines {
		p.data.Logs = p.data.Logs[len(p.data.Logs)-maxUpgradeLogLines:]
	}
}

func (p *upgradeProgressStore) Log(message string) {
	p.Lock()
	defer p.Unlock()
	p.appendLine(logLevelInfo, message)
}

func (p *upgradeProgressStore) Logf(format string, args ...interface{}) {
	p.Lock()
	defer p.Unlock()
	p.appendLine(logLevelInfo, fmt.Sprintf(format, args...))
}

func (p *upgradeProgressStore) Success(message string) {
	p.Lock()
	defer p.Unlock()
	p.appendLine(logLevelSuccess, message)
}

func (p *upgradeProgressStore) Warn(message string) {
	p.Lock()
	defer p.Unlock()
	p.appendLine(logLevelWarn, message)
}

func (p *upgradeProgressStore) Fail(message string) {
	p.Lock()
	defer p.Unlock()
	p.data.Running = false
	p.data.Failed = true
	p.data.Message = message
	p.appendLine(logLevelError, message)
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
	if snapshot.Logs != nil {
		snapshot.Logs = append([]dto.UpgradeLogLine(nil), snapshot.Logs...)
	}
	return snapshot
}
