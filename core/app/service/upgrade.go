package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
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

	baseDir := path.Join(global.CONF.Base.InstallDir, fmt.Sprintf("1panel/tmp/upgrade/%s", req.Version))
	downloadDir := path.Join(baseDir, "downloads")
	_ = os.RemoveAll(baseDir)
	originalDir := path.Join(baseDir, "original")
	if err := os.MkdirAll(downloadDir, os.ModePerm); err != nil {
		return err
	}
	if err := os.MkdirAll(originalDir, os.ModePerm); err != nil {
		return err
	}

	fileName := fmt.Sprintf("1panel-%s-%s-%s.tar.gz", req.Version, "linux", itemArch)
	directURL := u.resolvePackageURL(req.Version, fileName)
	downloadCandidates := u.buildDownloadCandidates(directURL)
	_ = settingRepo.Update("SystemStatus", "Upgrading")
	upgradeProgress.Start(req.Version, downloadCandidates)
	go func() {
		failUpgrade := func(action string, err error, rollbackStep int) {
			global.LOG.Errorf("%s failed, err: %v", action, err)
			upgradeProgress.Fail(fmt.Sprintf("%s: %s", action, err.Error()))
			if rollbackStep > 0 {
				u.handleRollback(originalDir, rollbackStep, svcInfo)
			}
			_ = settingRepo.Update("SystemStatus", "Free")
		}

		oldLang := ctl_conf.Load("LANGUAGE")
		upgradeProgress.SetStage(StageDownload)
		if err := files.DownloadFileWithMirrors(downloadCandidates, downloadDir+"/"+fileName,
			upgradeProgress.SetMirror, upgradeProgress.SetDownload); err != nil {
			failUpgrade("download service file", err, 0)
			return
		}
		global.LOG.Info("download all file successful!")
		defer func() {
			_ = os.Remove(downloadDir)
		}()
		upgradeProgress.SetStage(StageDecompress)
		if err := files.HandleUnTar(downloadDir+"/"+fileName, downloadDir, ""); err != nil {
			failUpgrade("decompress file", err, 0)
			return
		}
		tmpDir := downloadDir + "/" + strings.ReplaceAll(fileName, ".tar.gz", "")

		upgradeProgress.SetStage(StageBackup)
		if err := u.handleBackup(originalDir, svcInfo); err != nil {
			failUpgrade("backup original files", err, 0)
			return
		}
		itemLog := model.UpgradeLog{NodeID: 0, OldVersion: global.CONF.Base.Version, NewVersion: req.Version, BackupFile: baseDir}
		_ = upgradeLogRepo.Create(&itemLog)

		global.LOG.Info("backup original data successful, now start to upgrade!")

		upgradeProgress.SetStage(StageInstall)
		if err := files.CopyFileWithRename(path.Join(tmpDir, "1panel-core"), "/usr/local/bin/1panel-core"); err != nil {
			failUpgrade("upgrade 1panel-core", err, 1)
			return
		}
		if err := files.CopyFileWithRename(path.Join(tmpDir, "1panel-agent"), "/usr/local/bin/1panel-agent"); err != nil {
			failUpgrade("upgrade 1panel-agent", err, 1)
			return
		}

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
		} else {
			global.LOG.Warn("upgrade package has no 1pctl, keep the existing one")
		}
		initScriptPath := path.Join(tmpDir, "initscript")
		if _, err := os.Stat(initScriptPath); err == nil {
			if err := files.CopyItem(false, true, path.Join(initScriptPath, svcInfo.selCoreName), svcInfo.basePath); err != nil {
				failUpgrade("upgrade "+svcInfo.coreName, err, 3)
				return
			}
			if err := files.CopyItem(false, true, path.Join(initScriptPath, svcInfo.selAgentName), svcInfo.basePath); err != nil {
				failUpgrade("upgrade "+svcInfo.agentName, err, 3)
				return
			}
		} else {
			global.LOG.Warn("upgrade package has no initscript, keep the existing ones")
		}

		if _, err := os.Stat(path.Join(tmpDir, "lang")); err == nil {
			if err := files.CopyItem(true, true, path.Join(tmpDir, "lang"), "/usr/local/bin"); err != nil {
				failUpgrade("update language files", err, 4)
				return
			}
		} else {
			global.LOG.Warn("upgrade package has no lang files, keep the existing ones")
		}
		geoipPath := path.Join(global.CONF.Base.InstallDir, "1panel/geo/GeoIP.mmdb")
		if _, err := os.Stat(path.Join(tmpDir, "GeoIP.mmdb")); err == nil {
			if err := files.CopyFileWithRename(path.Join(tmpDir, "GeoIP.mmdb"), geoipPath); err != nil {
				failUpgrade("update GeoIP database", err, 4)
				return
			}
		} else {
			global.LOG.Warn("upgrade package has no GeoIP.mmdb, keep the existing one")
		}

		global.LOG.Info("upgrade successful!")
		dropBackupCopies()
		xpack.MultiNodeProvider.AutoUpgradeWithMaster()
		go writeLogs(req.Version)
		_ = settingRepo.Update("SystemVersion", req.Version)
		_ = global.AgentDB.Model(&model.Setting{}).Where("key = ?", "SystemVersion").Updates(map[string]interface{}{"value": req.Version}).Error
		// Fork packages only ship core/agent binaries and keep the existing
		// 1pctl. The running version on startup is read from ORIGINAL_VERSION
		// inside 1pctl, so it must be refreshed in place — otherwise the panel
		// resets the displayed version back to the old value after restart.
		if err := ctl_conf.UpdateInFile("/usr/local/bin/1pctl", "ORIGINAL_VERSION", normalizeVersionTag(req.Version)); err != nil {
			global.LOG.Warnf("sync ORIGINAL_VERSION in 1pctl failed, err: %v", err)
		}
		global.CONF.Base.Version = req.Version
		_ = os.RemoveAll(downloadDir)
		_ = settingRepo.Update("SystemStatus", "Free")

		upgradeProgress.SetStage(StageRestart)
		controller.RestartPanel(true, true, true)
	}()
	return nil
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
	if err := files.CopyItem(false, true, "/usr/local/bin/1panel-core", originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(false, true, "/usr/local/bin/1panel-agent", originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(false, true, "/usr/local/bin/1pctl", originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(true, true, "/usr/local/bin/lang", originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(false, true, path.Join(svcInfo.basePath, svcInfo.coreName), originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(false, true, path.Join(svcInfo.basePath, svcInfo.agentName), originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(true, true, path.Join(global.CONF.Base.InstallDir, "1panel/db"), originalDir); err != nil {
		return err
	}
	if err := files.CopyItem(false, true, path.Join(global.CONF.Base.InstallDir, "1panel/geo/GeoIP.mmdb"), originalDir); err != nil {
		return err
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
