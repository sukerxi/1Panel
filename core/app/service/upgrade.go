package service

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/1Panel-dev/1Panel/core/app/dto"
	"github.com/1Panel-dev/1Panel/core/app/model"
	"github.com/1Panel-dev/1Panel/core/app/repo"
	"github.com/1Panel-dev/1Panel/core/buserr"
	"github.com/1Panel-dev/1Panel/core/constant"
	"github.com/1Panel-dev/1Panel/core/global"
	"github.com/1Panel-dev/1Panel/core/utils/cmd"
	"github.com/1Panel-dev/1Panel/core/utils/common"
	"github.com/1Panel-dev/1Panel/core/utils/controller"
	"github.com/1Panel-dev/1Panel/core/utils/ctl_conf"
	"github.com/1Panel-dev/1Panel/core/utils/files"
	"github.com/1Panel-dev/1Panel/core/utils/req_helper"
	upgradeUtil "github.com/1Panel-dev/1Panel/core/utils/upgrade"
	"github.com/1Panel-dev/1Panel/core/utils/xpack"
)

type serviceInfo struct {
	basePath     string
	coreName     string
	agentName    string
	selCoreName  string
	selAgentName string
}

func loadServiceInfo() (serviceInfo, error) {
	basePath, err := controller.GetServicePath("")
	if err != nil {
		global.LOG.Errorf("get service path failed: %v", err)
		return serviceInfo{}, err
	}
	coreName, err := controller.LoadServiceName("1panel-core")
	if err != nil {
		global.LOG.Errorf("load core service name failed: %v", err)
		return serviceInfo{}, err
	}
	agentName, err := controller.LoadServiceName("1panel-agent")
	if err != nil {
		global.LOG.Errorf("load agent service name failed: %v", err)
		return serviceInfo{}, err
	}
	selCoreName, err := controller.SelectInitScript("1panel-core")
	if err != nil {
		global.LOG.Errorf("select core init script failed: %v", err)
		return serviceInfo{}, err
	}
	selAgentName, err := controller.SelectInitScript("1panel-agent")
	if err != nil {
		global.LOG.Errorf("select agent init script failed: %v", err)
		return serviceInfo{}, err
	}
	return serviceInfo{
		basePath:     basePath,
		coreName:     coreName,
		agentName:    agentName,
		selCoreName:  selCoreName,
		selAgentName: selAgentName,
	}, nil
}

type UpgradeService struct{}

type IUpgradeService interface {
	Upgrade(req dto.Upgrade) error
	UploadUpgradePackage(file *multipart.FileHeader) (dto.ManualPackageInfo, error)
	Rollback(req dto.OperateByID) error
	LoadNotes(req dto.Upgrade) (string, error)
	SearchUpgrade() (*dto.UpgradeInfo, error)
	LoadRelease() ([]dto.ReleasesNotes, error)
	LoadUpgradeProgress() dto.UpgradeProgress
}

func NewIUpgradeService() IUpgradeService {
	return &UpgradeService{}
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	Prerelease  bool                 `json:"prerelease"`
	Draft       bool                 `json:"draft"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

var githubReleaseCache = struct {
	sync.Mutex
	releases  []githubRelease
	expiresAt time.Time
}{}

// githubDownloadMirrors are ghproxy-compatible public mirrors probed before
// the direct GitHub URL. Public mirrors come and go (ghfast.top/ghproxy.net/
// homeboyc were all unreachable or rejecting requests in 2026-10), so every
// entry is health-checked at download time with a first-byte timeout and the
// direct URL always remains the last fallback.
var githubDownloadMirrors = []string{
	"https://gh-proxy.com/",
	"https://gh-proxy.org/",
}

// normalizeVersionTag accepts a GitHub tag (with or without a leading "v")
// and returns the canonical panel version string (e.g. "v2.3.2").
func normalizeVersionTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if tag[0] >= '0' && tag[0] <= '9' {
		return "v" + tag
	}
	return tag
}

func (u *UpgradeService) loadGithubReleases(forceRefresh bool) ([]githubRelease, error) {
	githubReleaseCache.Lock()
	defer githubReleaseCache.Unlock()
	if !forceRefresh && time.Now().Before(githubReleaseCache.expiresAt) && len(githubReleaseCache.releases) > 0 {
		return githubReleaseCache.releases, nil
	}
	requestURL := global.GithubAPIBaseURL() + "/releases?per_page=50"
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"User-Agent":           "1Panel-" + global.CONF.Base.Version,
		"X-GitHub-Api-Version": "2022-11-28",
	}
	status, res, err := u.requestGithubAPI(requestURL, headers)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("load github releases failed, http status: %d", status)
	}
	var releases []githubRelease
	if err := json.Unmarshal(res, &releases); err != nil {
		return nil, fmt.Errorf("parse github releases failed: %w", err)
	}
	filtered := make([]githubRelease, 0, len(releases))
	for _, item := range releases {
		if item.Draft || strings.TrimSpace(item.TagName) == "" {
			continue
		}
		filtered = append(filtered, item)
	}
	githubReleaseCache.releases = filtered
	githubReleaseCache.expiresAt = time.Now().Add(5 * time.Minute)
	return filtered, nil
}

func firstGithubRelease(releases []githubRelease, prerelease bool) *githubRelease {
	for i := range releases {
		if releases[i].Prerelease == prerelease {
			return &releases[i]
		}
	}
	return nil
}

func findGithubReleaseByVersion(releases []githubRelease, version string) *githubRelease {
	version = normalizeVersionTag(version)
	for i := range releases {
		if normalizeVersionTag(releases[i].TagName) == version {
			return &releases[i]
		}
	}
	return nil
}

func (u *UpgradeService) SearchUpgrade() (*dto.UpgradeInfo, error) {
	if global.CONF.Base.IsOffline {
		return &dto.UpgradeInfo{}, nil
	}
	var upgrade dto.UpgradeInfo
	currentVersion, err := settingRepo.Get(repo.WithByKey("SystemVersion"))
	if err != nil {
		return nil, err
	}
	developerMode, err := settingRepo.Get(repo.WithByKey("DeveloperMode"))
	if err != nil {
		return nil, err
	}

	releases, err := u.loadGithubReleases(false)
	if err != nil {
		// A failing version check (offline/proxy/rate limit) must not break the settings page.
		global.LOG.Errorf("load github releases for upgrade check failed, err: %v", err)
		return &upgrade, nil
	}
	developerEnabled := global.CONF.Base.Mode == "dev" || developerMode.Value == constant.StatusEnable
	if stable := firstGithubRelease(releases, false); stable != nil {
		version := normalizeVersionTag(stable.TagName)
		if common.ComparePanelVersion(version, currentVersion.Value) {
			upgrade.LatestVersion = version
		}
	}
	if developerEnabled {
		if beta := firstGithubRelease(releases, true); beta != nil {
			version := normalizeVersionTag(beta.TagName)
			if common.ComparePanelVersion(version, currentVersion.Value) {
				upgrade.TestVersion = version
			}
		}
	}

	itemVersion := upgrade.LatestVersion
	if developerEnabled && upgrade.TestVersion != "" {
		itemVersion = upgrade.TestVersion
	}
	if itemVersion == "" {
		return &upgrade, nil
	}
	if rel := findGithubReleaseByVersion(releases, itemVersion); rel != nil {
		upgrade.ReleaseNote = rel.Body
	}
	return &upgrade, nil
}

func (u *UpgradeService) LoadNotes(req dto.Upgrade) (string, error) {
	releases, err := u.loadGithubReleases(false)
	if err != nil {
		return "", err
	}
	rel := findGithubReleaseByVersion(releases, req.Version)
	if rel == nil {
		// A release may have been published after the cached response, refresh once.
		releases, err = u.loadGithubReleases(true)
		if err != nil {
			return "", err
		}
		rel = findGithubReleaseByVersion(releases, req.Version)
	}
	if rel == nil {
		return "", fmt.Errorf("release notes of version %s not found in github repository %s/%s", req.Version, global.GitHubOwner, global.GitHubRepo)
	}
	return rel.Body, nil
}

// upgradeTask carries everything the background upgrade goroutine needs.
type upgradeTask struct {
	version      string
	fileName     string
	arch         string
	svcInfo      serviceInfo
	baseDir      string
	downloadDir  string
	originalDir  string
	candidates   []files.DownloadCandidate
	directURL    string
	localPackage string // absolute path of an uploaded package; download is skipped when set
}

func (u *UpgradeService) Upgrade(req dto.Upgrade) error {
	global.LOG.Info("start to upgrade now...")
	if upgradeProgress.IsRunning() {
		return fmt.Errorf("an upgrade task is already running")
	}
	itemArch, err := loadArch()
	if err != nil {
		return err
	}
	svcInfo, err := loadServiceInfo()
	if err != nil {
		return err
	}
	if err := checkUpgradeSpace(); err != nil {
		return err
	}

	task := upgradeTask{
		arch:    itemArch,
		svcInfo: svcInfo,
	}
	if strings.TrimSpace(req.Package) != "" {
		localPath, meta, err := u.resolveManualPackage(req.Package, itemArch)
		if err != nil {
			return err
		}
		task.localPackage = localPath
		task.version = meta.Version
	} else {
		task.version = normalizeVersionTag(req.Version)
	}

	task.fileName = fmt.Sprintf("1panel-%s-linux-%s.tar.gz", task.version, itemArch)
	task.baseDir = path.Join(global.CONF.Base.InstallDir, fmt.Sprintf("1panel/tmp/upgrade/%s", task.version))
	task.downloadDir = path.Join(task.baseDir, "downloads")
	_ = os.RemoveAll(task.baseDir)
	task.originalDir = path.Join(task.baseDir, "original")
	if err := os.MkdirAll(task.downloadDir, os.ModePerm); err != nil {
		return err
	}
	if err := os.MkdirAll(task.originalDir, os.ModePerm); err != nil {
		return err
	}

	if task.localPackage == "" {
		task.directURL = u.resolvePackageURL(task.version, task.fileName)
		task.candidates = u.buildDownloadCandidates(task.directURL)
	}
	_ = settingRepo.Update("SystemStatus", "Upgrading")
	upgradeProgress.Start(task.version, task.candidates)
	go u.runUpgrade(task)
	return nil
}

func (u *UpgradeService) runUpgrade(task upgradeTask) {
	defer func() {
		_ = os.RemoveAll(task.downloadDir)
	}()
	failUpgrade := func(action string, err error, rollbackStep int) {
		global.LOG.Errorf("%s failed, err: %v", action, err)
		upgradeProgress.Fail(fmt.Sprintf("%s: %s", action, err.Error()))
		if rollbackStep > 0 {
			u.handleRollback(task.originalDir, rollbackStep, task.svcInfo)
		}
		_ = settingRepo.Update("SystemStatus", "Free")
	}

	oldLang := ctl_conf.Load("LANGUAGE")
	packagePath := path.Join(task.downloadDir, task.fileName)

	if task.localPackage != "" {
		upgradeProgress.SetStage(StagePreparing)
		upgradeProgress.Logf("Manual upgrade with uploaded package: %s", task.fileName)
		upgradeProgress.Log("Verifying package format and contents ...")
		if err := validateUpgradePackage(task.localPackage); err != nil {
			failUpgrade("verify uploaded package", err, 0)
			return
		}
		upgradeProgress.Logf("Staging uploaded package to %s ...", packagePath)
		if err := files.CopyFile(task.localPackage, packagePath, true); err != nil {
			failUpgrade("stage uploaded package", err, 0)
			return
		}
		upgradeProgress.Success("Uploaded package verified")
	} else {
		upgradeProgress.SetStage(StageDownload)
		upgradeProgress.Logf("Preparing upgrade to %s (linux/%s)", task.version, task.arch)
		upgradeProgress.Logf("Package file: %s", task.fileName)
		upgradeProgress.Logf("Download URL: %s", task.directURL)
		for i, candidate := range task.candidates {
			upgradeProgress.Logf("Source %d/%d: %s (%s)", i+1, len(task.candidates), candidate.Name, candidate.URL)
		}
		var lastLogPct int64
		onMirror := func(name, status, detail string) {
			upgradeProgress.SetMirror(name, status, detail)
			switch status {
			case files.MirrorProbing:
				upgradeProgress.Logf("Connecting to download source: %s ...", name)
			case files.MirrorFailed:
				upgradeProgress.Logf("Source %s failed: %s", name, detail)
				upgradeProgress.Log("Switching to the next download source ...")
			case files.MirrorSuccess:
				upgradeProgress.Success(fmt.Sprintf("Download source connected: %s", name))
			}
		}
		onProgress := func(downloaded, total, speedBps int64) {
			upgradeProgress.SetDownload(downloaded, total, speedBps)
			if total <= 0 {
				return
			}
			pct := downloaded * 100 / total
			if pct >= lastLogPct+10 || (pct == 100 && lastLogPct < 100) {
				if speedBps > 0 {
					upgradeProgress.Logf("Downloaded %s / %s (%d%%, %s/s)",
						humanSize(downloaded), humanSize(total), pct, humanSize(speedBps))
				} else {
					upgradeProgress.Logf("Downloaded %s / %s (%d%%)", humanSize(downloaded), humanSize(total), pct)
				}
				lastLogPct = pct
			}
		}
		upgradeProgress.Log("Starting download ...")
		if err := files.DownloadFileWithMirrors(task.candidates, packagePath, onMirror, onProgress); err != nil {
			failUpgrade("download service file", err, 0)
			return
		}
		global.LOG.Info("download all file successful!")
		if info, err := os.Stat(packagePath); err == nil {
			upgradeProgress.Success(fmt.Sprintf("Download completed (%s)", humanSize(info.Size())))
		} else {
			upgradeProgress.Success("Download completed")
		}
	}

	upgradeProgress.SetStage(StageDecompress)
	upgradeProgress.Logf("Extracting package: %s", packagePath)
	if err := files.HandleUnTar(packagePath, task.downloadDir, ""); err != nil {
		failUpgrade("decompress file", err, 0)
		return
	}
	tmpDir := path.Join(task.downloadDir, strings.ReplaceAll(task.fileName, ".tar.gz", ""))
	if entries, err := os.ReadDir(tmpDir); err == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		upgradeProgress.Logf("Package contents: %s", strings.Join(names, ", "))
	}
	for _, binary := range []string{"1panel-core", "1panel-agent"} {
		if _, err := os.Stat(path.Join(tmpDir, binary)); err != nil {
			failUpgrade("verify package contents", fmt.Errorf("required file %s not found in the package", binary), 0)
			return
		}
	}
	upgradeProgress.Success("Extraction completed")

	upgradeProgress.SetStage(StageBackup)
	upgradeProgress.Log("Backing up the current version for possible rollback ...")
	if err := u.handleBackup(task.originalDir, task.svcInfo); err != nil {
		failUpgrade("backup original files", err, 0)
		return
	}
	itemLog := model.UpgradeLog{NodeID: 0, OldVersion: global.CONF.Base.Version, NewVersion: task.version, BackupFile: task.baseDir}
	_ = upgradeLogRepo.Create(&itemLog)
	upgradeProgress.Success("Current version backed up: " + task.baseDir)

	global.LOG.Info("backup original data successful, now start to upgrade!")
	upgradeProgress.SetStage(StageInstall)
	upgradeProgress.Log("Installing new version files ...")
	if err := files.CopyFileWithRename(path.Join(tmpDir, "1panel-core"), "/usr/local/bin/1panel-core"); err != nil {
		failUpgrade("upgrade 1panel-core", err, 1)
		return
	}
	upgradeProgress.Log("Installed /usr/local/bin/1panel-core")
	if err := files.CopyFileWithRename(path.Join(tmpDir, "1panel-agent"), "/usr/local/bin/1panel-agent"); err != nil {
		failUpgrade("upgrade 1panel-agent", err, 1)
		return
	}
	upgradeProgress.Log("Installed /usr/local/bin/1panel-agent")

	// 1pctl, init scripts, lang files and GeoIP database are packaged by the
	// upstream installer repository. Fork builds may only ship the two binaries,
	// so every extra resource is upgraded only when the archive contains it.
	if _, err := os.Stat(path.Join(tmpDir, "1pctl")); err == nil {
		if err := files.CopyFileWithRename(path.Join(tmpDir, "1pctl"), "/usr/local/bin/1pctl"); err != nil {
			failUpgrade("upgrade 1pctl", err, 2)
			return
		}
		if err := ctl_conf.UpdateInFile("/usr/local/bin/1pctl", "BASE_DIR", global.CONF.Base.InstallDir); err != nil {
			failUpgrade("upgrade basedir in 1pctl", err, 2)
			return
		}
		if err := ctl_conf.UpdateInFile("/usr/local/bin/1pctl", "LANGUAGE", oldLang); err != nil {
			failUpgrade("upgrade language in 1pctl", err, 2)
			return
		}
		upgradeProgress.Log("Installed /usr/local/bin/1pctl")
	} else {
		global.LOG.Warn("upgrade package has no 1pctl, keep the existing one")
		upgradeProgress.Log("Package has no 1pctl, keeping the current one")
	}
	initScriptPath := path.Join(tmpDir, "initscript")
	if _, err := os.Stat(initScriptPath); err == nil {
		if err := files.CopyItem(false, true, path.Join(initScriptPath, task.svcInfo.selCoreName), task.svcInfo.basePath); err != nil {
			failUpgrade("upgrade "+task.svcInfo.coreName, err, 3)
			return
		}
		if err := files.CopyItem(false, true, path.Join(initScriptPath, task.svcInfo.selAgentName), task.svcInfo.basePath); err != nil {
			failUpgrade("upgrade "+task.svcInfo.agentName, err, 3)
			return
		}
		upgradeProgress.Logf("Updated service init scripts: %s, %s", task.svcInfo.coreName, task.svcInfo.agentName)
	} else {
		global.LOG.Warn("upgrade package has no initscript, keep the existing ones")
		upgradeProgress.Log("Package has no initscript, keeping the current ones")
	}

	if _, err := os.Stat(path.Join(tmpDir, "lang")); err == nil {
		if err := files.CopyItem(true, true, path.Join(tmpDir, "lang"), "/usr/local/bin"); err != nil {
			failUpgrade("update language files", err, 4)
			return
		}
		upgradeProgress.Log("Updated language files")
	} else {
		global.LOG.Warn("upgrade package has no lang files, keep the existing ones")
		upgradeProgress.Log("Package has no lang files, keeping the current ones")
	}
	geoipPath := path.Join(global.CONF.Base.InstallDir, "1panel/geo/GeoIP.mmdb")
	if _, err := os.Stat(path.Join(tmpDir, "GeoIP.mmdb")); err == nil {
		if err := files.CopyFileWithRename(path.Join(tmpDir, "GeoIP.mmdb"), geoipPath); err != nil {
			failUpgrade("update GeoIP database", err, 4)
			return
		}
		upgradeProgress.Log("Updated GeoIP database")
	} else {
		global.LOG.Warn("upgrade package has no GeoIP.mmdb, keep the existing one")
		upgradeProgress.Log("Package has no GeoIP.mmdb, keeping the current one")
	}

	global.LOG.Info("upgrade successful!")
	upgradeProgress.Success(fmt.Sprintf("All files installed, version %s is ready", task.version))
	dropBackupCopies()
	xpack.MultiNodeProvider.AutoUpgradeWithMaster()
	go writeLogs(task.version)
	_ = settingRepo.Update("SystemVersion", task.version)
	_ = global.AgentDB.Model(&model.Setting{}).Where("key = ?", "SystemVersion").Updates(map[string]interface{}{"value": task.version}).Error
	// Fork packages only ship core/agent binaries and keep the existing
	// 1pctl. The running version on startup is read from ORIGINAL_VERSION
	// inside 1pctl, so it must be refreshed in place — otherwise the panel
	// resets the displayed version back to the old value after restart.
	if err := ctl_conf.UpdateInFile("/usr/local/bin/1pctl", "ORIGINAL_VERSION", normalizeVersionTag(task.version)); err != nil {
		global.LOG.Warnf("sync ORIGINAL_VERSION in 1pctl failed, err: %v", err)
		upgradeProgress.Warn(fmt.Sprintf("Sync version in 1pctl failed: %v", err))
	}
	global.CONF.Base.Version = task.version
	if task.localPackage != "" {
		// The staged copy is no longer needed once installation succeeded.
		if err := os.Remove(task.localPackage); err != nil && !os.IsNotExist(err) {
			global.LOG.Warnf("remove staged manual package failed, err: %v", err)
		}
	}
	_ = settingRepo.Update("SystemStatus", "Free")

	upgradeProgress.SetStage(StageRestart)
	upgradeProgress.Log("Restarting 1panel-core and 1panel-agent services ...")
	controller.RestartPanel(true, true, true)
}

func (u *UpgradeService) Rollback(req dto.OperateByID) error {
	log, _ := upgradeLogRepo.Get(repo.WithByID(req.ID))
	if log.ID == 0 {
		return buserr.New("ErrRecordNotFound")
	}
	svcInfo, err := loadServiceInfo()
	if err != nil {
		return err
	}
	u.handleRollback(log.BackupFile, 3, svcInfo)
	return nil
}

// LoadUpgradeProgress returns the live upgrade progress for polling.
func (u *UpgradeService) LoadUpgradeProgress() dto.UpgradeProgress {
	return LoadUpgradeProgress()
}

func formatGithubPublishTime(t string) string {
	if t == "" {
		return ""
	}
	publishedAt, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return ""
	}
	return publishedAt.Format("2006-01-02")
}

func (u *UpgradeService) LoadRelease() ([]dto.ReleasesNotes, error) {
	var notes []dto.ReleasesNotes
	releases, err := u.loadGithubReleases(false)
	if err != nil {
		return notes, err
	}
	for _, item := range releases {
		notes = append(notes, dto.ReleasesNotes{
			Version:   normalizeVersionTag(item.TagName),
			Content:   item.Body,
			CreatedAt: formatGithubPublishTime(item.PublishedAt),
		})
	}
	return notes, nil
}

func (u *UpgradeService) handleBackup(originalDir string, svcInfo serviceInfo) error {
	backupSteps := []struct {
		src   string
		isDir bool
	}{
		{"/usr/local/bin/1panel-core", false},
		{"/usr/local/bin/1panel-agent", false},
		{"/usr/local/bin/1pctl", false},
		{"/usr/local/bin/lang", true},
		{path.Join(svcInfo.basePath, svcInfo.coreName), false},
		{path.Join(svcInfo.basePath, svcInfo.agentName), false},
		{path.Join(global.CONF.Base.InstallDir, "1panel/db"), true},
		{path.Join(global.CONF.Base.InstallDir, "1panel/geo/GeoIP.mmdb"), false},
	}
	for _, step := range backupSteps {
		upgradeProgress.Logf("Backing up %s ...", step.src)
		if err := files.CopyItem(step.isDir, true, step.src, originalDir); err != nil {
			return err
		}
	}
	return nil
}

func (u *UpgradeService) handleRollback(originalDir string, errStep int, svcInfo serviceInfo) {
	// The restored 1pctl carries the pre-upgrade ORIGINAL_VERSION. Sync the
	// database after restoring files so startup version detection cannot
	// mistake a rollback for a freshly finished upgrade.
	defer func() {
		if rollbackVersion, err := ctl_conf.LoadFromFile("/usr/local/bin/1pctl", "ORIGINAL_VERSION"); err == nil &&
			rollbackVersion != "" && rollbackVersion != `""` {
			_ = settingRepo.Update("SystemVersion", rollbackVersion)
			global.CONF.Base.Version = rollbackVersion
		}
	}()
	_ = settingRepo.Update("SystemStatus", "Free")
	dbPath := path.Join(global.CONF.Base.InstallDir, "1panel")
	if _, err := os.Stat(path.Join(originalDir, "db")); err == nil {
		if err := files.CopyItem(true, true, path.Join(originalDir, "db"), dbPath); err != nil {
			global.LOG.Errorf("rollback 1panel db failed, err: %v", err)
		}
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, "1panel-core"), "/usr/local/bin/1panel-core"); err != nil {
		global.LOG.Errorf("rollback 1panel-core failed, err: %v", err)
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, "1panel-agent"), "/usr/local/bin/1panel-agent"); err != nil {
		global.LOG.Errorf("rollback 1panel-agent failed, err: %v", err)
	}
	if errStep == 1 {
		return
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, "1pctl"), "/usr/local/bin/1pctl"); err != nil {
		global.LOG.Errorf("rollback 1pctl failed, err: %v", err)
	}
	if errStep == 2 {
		return
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, svcInfo.coreName), path.Join(svcInfo.basePath, svcInfo.coreName)); err != nil {
		global.LOG.Errorf("rollback %s failed, err: %v", svcInfo.coreName, err)
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, svcInfo.agentName), path.Join(svcInfo.basePath, svcInfo.agentName)); err != nil {
		global.LOG.Errorf("rollback %s failed, err: %v", svcInfo.agentName, err)
	}
	if errStep == 3 {
		return
	}
	if err := files.CopyItem(true, true, path.Join(originalDir, "lang"), "/usr/local/bin"); err != nil {
		global.LOG.Errorf("rollback language files failed, err: %v", err)
	}
	if err := files.CopyFileWithRename(path.Join(originalDir, "GeoIP.mmdb"), path.Join(global.CONF.Base.InstallDir, "1panel/geo/GeoIP.mmdb")); err != nil {
		global.LOG.Errorf("rollback GeoIP database failed, err: %v", err)
	}
}

func (u *UpgradeService) resolvePackageURL(version, fileName string) string {
	releases, err := u.loadGithubReleases(false)
	if err != nil {
		global.LOG.Warnf("load github releases when resolving package failed, err: %v", err)
	} else if rel := findGithubReleaseByVersion(releases, version); rel != nil {
		for _, asset := range rel.Assets {
			if asset.Name == fileName && asset.BrowserDownloadURL != "" &&
				strings.Contains(asset.BrowserDownloadURL, "github.com/") {
				return asset.BrowserDownloadURL
			}
		}
	}
	// Fallback to the standard GitHub release asset URL convention.
	return fmt.Sprintf("%s/%s/%s", global.GithubReleaseDownloadURL(), normalizeVersionTag(version), fileName)
}

// buildDownloadCandidates lists ghproxy mirrors first and the direct GitHub
// URL last. Mirrors are probed in order during download.
func (u *UpgradeService) buildDownloadCandidates(directURL string) []files.DownloadCandidate {
	candidates := make([]files.DownloadCandidate, 0, len(githubDownloadMirrors)+1)
	source := strings.TrimPrefix(directURL, "https://")
	source = strings.TrimPrefix(source, "http://")
	for _, prefix := range githubDownloadMirrors {
		base := strings.TrimSuffix(prefix, "/")
		candidates = append(candidates, files.DownloadCandidate{
			Name: files.CandidateName(base),
			URL:  base + "/" + source,
		})
	}
	candidates = append(candidates, files.DownloadCandidate{Name: "github.com", URL: directURL})
	return candidates
}

// requestGithubAPI calls api.github.com directly first and, when that fails
// (common on networks where GitHub is throttled), retries through every
// ghproxy-compatible mirror. Mirrors which do not support the API simply
// return a non-200/non-JSON response and are skipped.
func (u *UpgradeService) requestGithubAPI(requestURL string, headers map[string]string) (int, []byte, error) {
	candidates := make([]string, 0, len(githubDownloadMirrors)+1)
	candidates = append(candidates, requestURL)
	for _, prefix := range githubDownloadMirrors {
		candidates = append(candidates, strings.TrimSuffix(prefix, "/")+"/"+requestURL)
	}
	var lastErr error
	for _, candidateURL := range candidates {
		status, res, err := req_helper.HandleRequestWithProxyHeaders(candidateURL, http.MethodGet, constant.TimeOut20s, headers)
		if err == nil && status == http.StatusOK {
			return status, res, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("http status: %d", status)
		}
		global.LOG.Warnf("github api request via %s failed, err: %v", candidateURL, lastErr)
	}
	return 0, nil, lastErr
}

func loadArch() (string, error) {
	std, err := cmd.NewCommandMgr().RunWithStdout("uname", "-a")
	if err != nil {
		return "", fmt.Errorf("std: %s, err: %s", std, err.Error())
	}
	if strings.Contains(std, "x86_64") {
		return "amd64", nil
	}
	if strings.Contains(std, "arm64") || strings.Contains(std, "aarch64") {
		return "arm64", nil
	}
	if strings.Contains(std, "armv7l") {
		return "armv7", nil
	}
	if strings.Contains(std, "ppc64le") {
		return "ppc64le", nil
	}
	if strings.Contains(std, "s390x") {
		return "s390x", nil
	}
	if strings.Contains(std, "riscv64") {
		return "riscv64", nil
	}
	return "", fmt.Errorf("unsupported such arch: %s", std)
}

func dropBackupCopies() {
	backupCopies, _ := settingRepo.GetValueByKey("UpgradeBackupCopies")
	if err := upgradeUtil.DropBackupCopies(global.CONF.Base.InstallDir, backupCopies); err != nil {
		global.LOG.Errorf("read upgrade dir failed, err: %v", err)
	}
}

// manualPackageMinSize guards against error pages / truncated uploads: the
// two stripped Go binaries in a fork package are tens of megabytes.
const manualPackageMinSize = 1 << 20

// manualPackageReg matches release asset names, e.g.
// 1panel-v2.3.2-linux-amd64.tar.gz or 1panel-v2.3.2-beta.1-linux-arm64.tar.gz.
var manualPackageReg = regexp.MustCompile(
	`^1panel-(v?[0-9][0-9A-Za-z.]*(?:-[0-9A-Za-z.]+)?)-linux-(amd64|arm64|armv7|ppc64le|s390x|riscv64)\.tar\.gz$`)

type manualPackageMeta struct {
	FileName string
	Version  string
	Arch     string
}

// manualPackageDir returns the staging directory for manually uploaded packages.
// It is a sibling of the per-version upgrade dirs, so wiping one version's
// upgrade dir never removes staged packages.
func manualPackageDir() string {
	return path.Join(global.CONF.Base.InstallDir, "1panel/tmp/upgrade/manual")
}

func parseManualPackageName(name string) (manualPackageMeta, error) {
	matches := manualPackageReg.FindStringSubmatch(filepath.Base(name))
	if matches == nil {
		return manualPackageMeta{}, fmt.Errorf("invalid package name %q, expected 1panel-<version>-linux-<arch>.tar.gz", name)
	}
	return manualPackageMeta{
		FileName: matches[0],
		Version:  normalizeVersionTag(matches[1]),
		Arch:     matches[2],
	}, nil
}

// validateUpgradePackage verifies that the uploaded file is a gzip tar archive
// containing the required panel binaries. It streams the archive instead of
// trusting the file extension alone.
func validateUpgradePackage(packagePath string) error {
	info, err := os.Stat(packagePath)
	if err != nil {
		return fmt.Errorf("stat package failed: %w", err)
	}
	if info.Size() < manualPackageMinSize {
		return fmt.Errorf("package is only %s, it is likely truncated or not a real upgrade package", humanSize(info.Size()))
	}
	file, err := os.Open(packagePath)
	if err != nil {
		return fmt.Errorf("open package failed: %w", err)
	}
	defer file.Close()

	header := make([]byte, 2)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read package header failed: %w", err)
	}
	if header[0] != 0x1f || header[1] != 0x8b {
		return errors.New("file is not a valid gzip tar package")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind package failed: %w", err)
	}
	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip reader failed: %w", err)
	}
	defer gzReader.Close()

	required := map[string]bool{"1panel-core": false, "1panel-agent": false}
	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read package entries failed: %w", err)
		}
		base := filepath.Base(header.Name)
		if _, ok := required[base]; ok {
			required[base] = true
		}
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("invalid upgrade package: %s is missing", name)
		}
	}
	return nil
}

// UploadUpgradePackage stores an uploaded release package in the staging
// directory and validates it before it can be used for a manual upgrade.
func (u *UpgradeService) UploadUpgradePackage(file *multipart.FileHeader) (dto.ManualPackageInfo, error) {
	if upgradeProgress.IsRunning() {
		return dto.ManualPackageInfo{}, fmt.Errorf("an upgrade task is already running")
	}
	if file == nil {
		return dto.ManualPackageInfo{}, fmt.Errorf("no package file provided")
	}
	meta, err := parseManualPackageName(file.Filename)
	if err != nil {
		return dto.ManualPackageInfo{}, err
	}
	hostArch, err := loadArch()
	if err != nil {
		return dto.ManualPackageInfo{}, err
	}
	if meta.Arch != hostArch {
		return dto.ManualPackageInfo{}, fmt.Errorf("package arch %s does not match the host arch %s", meta.Arch, hostArch)
	}

	stagingDir := manualPackageDir()
	if err := os.MkdirAll(stagingDir, os.ModePerm); err != nil {
		return dto.ManualPackageInfo{}, err
	}
	dst := filepath.Join(stagingDir, meta.FileName)
	src, err := file.Open()
	if err != nil {
		return dto.ManualPackageInfo{}, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, constant.FilePerm)
	if err != nil {
		_ = src.Close()
		return dto.ManualPackageInfo{}, err
	}
	_, copyErr := io.Copy(out, src)
	_ = out.Close()
	_ = src.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return dto.ManualPackageInfo{}, fmt.Errorf("save uploaded package failed: %w", copyErr)
	}
	if err := validateUpgradePackage(dst); err != nil {
		_ = os.Remove(dst)
		return dto.ManualPackageInfo{}, err
	}
	global.LOG.Infof("manual upgrade package uploaded: %s (%s)", meta.FileName, humanSize(file.Size))
	return dto.ManualPackageInfo{
		Version:  meta.Version,
		Package:  meta.FileName,
		FileName: meta.FileName,
		Size:     file.Size,
	}, nil
}

// resolveManualPackage turns a staged file name into its absolute path and
// re-validates the file before the upgrade starts.
func (u *UpgradeService) resolveManualPackage(packageName, hostArch string) (string, manualPackageMeta, error) {
	meta, err := parseManualPackageName(packageName)
	if err != nil {
		return "", manualPackageMeta{}, err
	}
	if meta.Arch != hostArch {
		return "", manualPackageMeta{}, fmt.Errorf("package arch %s does not match the host arch %s", meta.Arch, hostArch)
	}
	stagingDir := manualPackageDir()
	packagePath := filepath.Join(stagingDir, meta.FileName)
	if !isWithinPath(stagingDir, packagePath) {
		return "", manualPackageMeta{}, fmt.Errorf("invalid package path: %s", packageName)
	}
	if _, err := os.Stat(packagePath); err != nil {
		return "", manualPackageMeta{}, fmt.Errorf("uploaded package %s is not found on the server, please upload it again", meta.FileName)
	}
	if err := validateUpgradePackage(packagePath); err != nil {
		return "", manualPackageMeta{}, err
	}
	return packagePath, meta, nil
}

// isWithinPath reports whether target is located inside dir. Both inputs must
// be cleaned absolute paths produced by filepath.Join.
func isWithinPath(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func humanSize(size int64) string {
	if size <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", size, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}
