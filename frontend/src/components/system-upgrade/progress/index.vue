<template>
    <el-dialog
        v-model="visible"
        width="760px"
        :close-on-click-modal="false"
        :close-on-press-escape="false"
        :show-close="progress.failed"
        @close="handleClose"
    >
        <template #header>
            <div class="dialog-header">
                <span class="dialog-title">{{ $t('setting.upgradeProgress.title', { version: version }) }}</span>
                <span class="dialog-subtitle">{{ $t('setting.upgradeProgress.subtitle') }}</span>
            </div>
        </template>
        <div class="upgrade-progress">
            <div class="steps">
                <template v-for="(stageKey, idx) in STAGES" :key="stageKey">
                    <div class="step" :class="stepState(idx)">
                        <div class="step-dot">
                            <el-icon v-if="stepState(idx) === 'done'"><Check /></el-icon>
                            <el-icon v-else-if="stepState(idx) === 'active'">
                                <component :is="stepMeta(idx).icon" />
                            </el-icon>
                            <el-icon v-else-if="stepState(idx) === 'error'"><Close /></el-icon>
                            <span v-else class="step-num">{{ idx + 1 }}</span>
                        </div>
                        <span class="step-label">{{ $t(`setting.upgradeProgress.${stepMeta(idx).labelKey}`) }}</span>
                    </div>
                    <div v-if="idx < STAGES.length - 1" class="step-line" :class="{ done: idx < activeStep }"></div>
                </template>
            </div>

            <div class="status-card" :class="{ 'is-error': progress.failed }">
                <div class="status-icon">
                    <el-icon v-if="progress.failed"><CircleClose /></el-icon>
                    <el-icon v-else class="is-loading"><Loading /></el-icon>
                </div>
                <div class="status-main">
                    <div class="status-headline">{{ headlineText }}</div>
                    <div class="status-sub">{{ statusSubText }}</div>
                    <div v-if="isDownloadPhase && progress.mirrors.length" class="mirror-chips">
                        <el-tooltip
                            v-for="item in progress.mirrors"
                            :key="item.name"
                            :content="item.status === 'failed' && item.detail ? item.detail : mirrorName(item.name)"
                            placement="top"
                        >
                            <span class="mirror-chip" :class="chipClass(item.status)">
                                <el-icon
                                    v-if="item.status === 'probing' || item.status === 'downloading'"
                                    class="is-loading chip-icon"
                                >
                                    <Loading />
                                </el-icon>
                                <i v-else class="chip-dot"></i>
                                <span class="chip-name">{{ mirrorName(item.name) }}</span>
                            </span>
                        </el-tooltip>
                    </div>
                    <div v-if="isDownloadPhase && !progress.failed" class="progress-row">
                        <el-progress
                            :percentage="progress.total > 0 ? percent : 100"
                            :stroke-width="8"
                            :show-text="false"
                            :indeterminate="progress.total <= 0"
                            :duration="2.5"
                        />
                    </div>
                </div>
                <div v-if="isDownloadPhase && progress.total > 0 && !progress.failed" class="status-percent">
                    {{ percent }}%
                </div>
            </div>

            <div class="log-panel">
                <div class="log-header">
                    <div class="log-file">
                        <span class="traffic red"></span>
                        <span class="traffic yellow"></span>
                        <span class="traffic green"></span>
                        <span class="log-filename">upgrade.log</span>
                    </div>
                    <div class="log-actions">
                        <span class="log-count">{{ progress.logs.length }} {{ $t('setting.upgradeProgress.logLines') }}</span>
                        <el-button text size="small" :disabled="!progress.logs.length" @click="copyLogs">
                            <el-icon class="copy-icon"><Check v-if="copied" /><CopyDocument v-else /></el-icon>
                            {{ copied ? $t('setting.upgradeProgress.copied') : $t('setting.upgradeProgress.copyLog') }}
                        </el-button>
                    </div>
                </div>
                <div ref="logBoxRef" class="log-box">
                    <div v-if="!progress.logs.length" class="log-empty">$ {{ $t('setting.upgradeProgress.waitingOutput') }}</div>
                    <div v-for="(line, idx) in progress.logs" :key="idx" class="log-line" :class="`is-${line.level}`">
                        <span class="log-time">{{ line.time.slice(5) }}</span>
                        <span class="log-level">{{ levelText(line.level) }}</span>
                        <span class="log-msg">{{ line.message }}</span>
                    </div>
                </div>
            </div>
        </div>
        <template #footer>
            <div class="dialog-footer">
                <el-button v-if="progress.failed" type="primary" @click="visible = false">
                    {{ $t('commons.button.close') }}
                </el-button>
                <span v-else class="footer-tip">
                    <el-icon class="footer-icon"><WarningFilled /></el-icon>
                    {{ $t('setting.upgradeProgress.pleaseWait') }}
                </span>
            </div>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { getUpgradeProgress } from '@/api/modules/setting';
import { Setting } from '@/api/interface/setting';
import { useGlobalStore } from '@/composables/useGlobalStore';
import {
    Check,
    CircleClose,
    Close,
    CopyDocument,
    DocumentCopy,
    Download,
    Files,
    Loading,
    RefreshRight,
    Tools,
    Upload,
    WarningFilled,
} from '@element-plus/icons-vue';
import i18n from '@/lang';
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';

const { isLoading, isOnRestart } = useGlobalStore();

const STAGES = ['download', 'decompress', 'backup', 'install', 'restart'] as const;

const STAGE_META: Record<string, { icon: unknown; labelKey: string; textKey: string }> = {
    download: { icon: Download, labelKey: 'stageDownload', textKey: 'stageText_download' },
    upload: { icon: Upload, labelKey: 'stageUpload', textKey: 'stageText_preparing' },
    decompress: { icon: Files, labelKey: 'stageDecompress', textKey: 'stageText_decompress' },
    backup: { icon: DocumentCopy, labelKey: 'stageBackup', textKey: 'stageText_backup' },
    install: { icon: Tools, labelKey: 'stageInstall', textKey: 'stageText_install' },
    restart: { icon: RefreshRight, labelKey: 'stageRestart', textKey: 'stageText_restart' },
};

// Manual uploads skip the download step: the first step renders as "upload".
const stepMeta = (idx: number) => {
    if (idx === 0 && progress.value.manual) {
        return STAGE_META.upload;
    }
    return STAGE_META[STAGES[idx]];
};

const visible = ref(false);
const version = ref('');
const copied = ref(false);
const logBoxRef = ref<HTMLElement>();
const progress = ref<Setting.UpgradeProgress>({
    running: false,
    failed: false,
    manual: false,
    stage: 'preparing',
    version: '',
    message: '',
    mirrors: [],
    downloaded: 0,
    total: 0,
    speedBps: 0,
    logs: [],
});

let pollTimer: ReturnType<typeof setInterval> | null = null;
let copiedTimer: ReturnType<typeof setTimeout> | null = null;

const LEVEL_TEXT: Record<string, string> = {
    info: 'INFO',
    success: ' OK ',
    warn: 'WARN',
    error: 'ERR ',
};

const levelText = (level: string) => LEVEL_TEXT[level] || 'INFO';

const scrollLogToBottom = () => {
    nextTick(() => {
        const box = logBoxRef.value;
        if (box) {
            box.scrollTop = box.scrollHeight;
        }
    });
};

watch(
    () => progress.value.logs.length,
    () => scrollLogToBottom(),
);

const percent = computed(() => {
    if (!progress.value.total || progress.value.total <= 0) return 0;
    return Math.min(100, Math.floor((progress.value.downloaded / progress.value.total) * 100));
});

const activeStep = computed(() => {
    const idx = STAGES.indexOf(progress.value.stage as (typeof STAGES)[number]);
    return idx < 0 ? 0 : idx;
});

// preparing only happens for manual uploads; the online flow starts directly
// in the download stage.
const isDownloadPhase = computed(() => progress.value.stage === 'download');

const currentMeta = computed(() => {
    if (progress.value.stage === 'preparing') {
        return progress.value.manual ? STAGE_META.upload : STAGE_META.download;
    }
    return STAGE_META[progress.value.stage] || STAGE_META.download;
});

const stepState = (idx: number): 'done' | 'active' | 'waiting' | 'error' => {
    if (progress.value.failed && idx === activeStep.value) return 'error';
    if (idx < activeStep.value) return 'done';
    if (idx === activeStep.value) return 'active';
    return 'waiting';
};

const lastLogMessage = computed(() => {
    for (let i = progress.value.logs.length - 1; i >= 0; i--) {
        if (progress.value.logs[i].level !== 'error') {
            return progress.value.logs[i].message;
        }
    }
    return '';
});

const activeMirror = computed(() =>
    progress.value.mirrors.find((item) => item.status === 'probing' || item.status === 'downloading'),
);

const headlineText = computed(() => {
    if (progress.value.failed) {
        return i18n.global.t('setting.upgradeProgress.failed');
    }
    return i18n.global.t(`setting.upgradeProgress.${currentMeta.value.textKey}`);
});

const statusSubText = computed(() => {
    if (progress.value.failed) {
        return progress.value.message;
    }
    if (progress.value.stage === 'preparing') {
        // preparing only exists for manual uploads. The online flow must never
        // show "verifying package" wording — it is a transient local/race
        // state before the first progress poll returns the download stage.
        if (progress.value.manual) {
            return lastLogMessage.value || i18n.global.t('setting.upgradeProgress.verifyingManual');
        }
        return i18n.global.t('setting.upgradeProgress.preparingDownload');
    }
    if (isDownloadPhase.value) {
        if (progress.value.total > 0) {
            const parts: string[] = [];
            if (activeMirror.value) {
                parts.push(mirrorName(activeMirror.value.name));
            }
            parts.push(`${formatBytes(progress.value.downloaded)} / ${formatBytes(progress.value.total)}`);
            if (progress.value.speedBps > 0) {
                parts.push(`${formatBytes(progress.value.speedBps)}/s`);
            }
            return parts.join('  ·  ');
        }
        if (activeMirror.value) {
            return i18n.global.t('setting.upgradeProgress.connecting', { name: mirrorName(activeMirror.value.name) });
        }
        return i18n.global.t('setting.upgradeProgress.preparingDownload');
    }
    return lastLogMessage.value;
});

const chipClass = (status: string) => ({
    'is-active': status === 'probing' || status === 'downloading',
    'is-success': status === 'success',
    'is-failed': status === 'failed',
});

const mirrorName = (name: string) => {
    if (name === 'github.com') {
        return 'GitHub';
    }
    return name;
};

const copyLogs = async () => {
    const text = progress.value.logs
        .map((line) => `[${line.time}] ${levelText(line.level).trim()} ${line.message}`)
        .join('\n');
    try {
        await navigator.clipboard.writeText(text);
        copied.value = true;
        if (copiedTimer) clearTimeout(copiedTimer);
        copiedTimer = setTimeout(() => {
            copied.value = false;
        }, 2000);
    } catch {
        // Clipboard may be unavailable on non-secure origins; the logs remain
        // selectable inside the terminal panel.
    }
};

const formatBytes = (bytes: number) => {
    if (!bytes || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / Math.pow(1024, exponent)).toFixed(1)} ${units[exponent]}`;
};

const stopPolling = () => {
    if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
    }
};

const handleRestart = () => {
    stopPolling();
    visible.value = false;
    // Hand over to the global reconnect flow that waits for the panel to come back.
    isLoading.value = true;
    isOnRestart.value = true;
};

const pollOnce = async () => {
    try {
        const res = await getUpgradeProgress();
        if (!res.data) return;
        progress.value = res.data;
        if (res.data.failed) {
            stopPolling();
            return;
        }
        if (res.data.stage === 'restart') {
            handleRestart();
        }
    } catch {
        // The panel may briefly reject requests during restart; the global
        // reconnect flow takes over once the restart stage is reached.
    }
};

const startPolling = () => {
    stopPolling();
    pollOnce();
    pollTimer = setInterval(pollOnce, 1000);
};

const start = (targetVersion: string, manual = false) => {
    version.value = targetVersion;
    progress.value = {
        running: true,
        failed: false,
        manual,
        // Only manual uploads start in the preparing/verifying phase; online
        // upgrades begin at download immediately.
        stage: manual ? 'preparing' : 'download',
        version: targetVersion,
        message: '',
        mirrors: [],
        downloaded: 0,
        total: 0,
        speedBps: 0,
        logs: [],
    };
    visible.value = true;
    startPolling();
};

// Reopen the dialog automatically when the page was refreshed mid-upgrade.
const resumeIfRunning = async () => {
    try {
        const res = await getUpgradeProgress();
        if (res.data && res.data.running) {
            version.value = res.data.version;
            progress.value = res.data;
            visible.value = true;
            if (res.data.stage === 'restart') {
                handleRestart();
            } else {
                startPolling();
            }
        }
    } catch {
        // ignore: upgrade endpoint is unavailable in offline mode
    }
};

const handleClose = () => {
    stopPolling();
};

onBeforeUnmount(() => {
    stopPolling();
    if (copiedTimer) {
        clearTimeout(copiedTimer);
    }
});

defineExpose({
    start,
    resumeIfRunning,
});
</script>

<style lang="scss" scoped>
.dialog-header {
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.dialog-title {
    font-size: 16px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    line-height: 22px;
}
.dialog-subtitle {
    font-size: 12.5px;
    font-weight: 400;
    color: var(--el-text-color-secondary);
    line-height: 18px;
}

.upgrade-progress {
    padding: 6px 4px 0;
}

/* ---------- custom step indicator ---------- */
.steps {
    display: flex;
    align-items: flex-start;
    padding: 6px 6px 0;
}
.step {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    width: 56px;
    flex-shrink: 0;
}
.step-dot {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: 50%;
    font-size: 15px;
    background-color: var(--el-fill-color);
    border: 1px solid var(--el-border-color);
    color: var(--el-text-color-secondary);
    transition: all 0.25s ease;
    .el-icon {
        font-size: 15px;
    }
}
.step-num {
    font-size: 13px;
    font-weight: 500;
}
.step-label {
    font-size: 12.5px;
    line-height: 18px;
    white-space: nowrap;
    color: var(--el-text-color-secondary);
    transition: color 0.25s ease;
}
.step-line {
    flex: 1;
    height: 2px;
    margin: 14px 6px 0;
    border-radius: 1px;
    background-color: var(--el-border-color);
    transition: background-color 0.3s ease;
    &.done {
        background-color: var(--el-color-success);
    }
}
.step.done {
    .step-dot {
        background-color: var(--el-color-success);
        border-color: var(--el-color-success);
        color: #fff;
    }
    .step-label {
        color: var(--el-text-color-primary);
    }
}
.step.active {
    .step-dot {
        background-color: var(--el-color-primary);
        border-color: var(--el-color-primary);
        color: #fff;
        animation: step-pulse 2s ease-out infinite;
    }
    .step-label {
        color: var(--el-color-primary);
        font-weight: 600;
    }
}
.step.error {
    .step-dot {
        background-color: var(--el-color-danger);
        border-color: var(--el-color-danger);
        color: #fff;
    }
    .step-label {
        color: var(--el-color-danger);
        font-weight: 600;
    }
}
@keyframes step-pulse {
    0% {
        box-shadow: 0 0 0 0 var(--el-color-primary-light-5);
    }
    70% {
        box-shadow: 0 0 0 9px transparent;
    }
    100% {
        box-shadow: 0 0 0 0 transparent;
    }
}

/* ---------- status card ---------- */
.status-card {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    margin-top: 24px;
    padding: 16px 18px;
    border-radius: 10px;
    border: 1px solid var(--el-border-color-lighter);
    background-color: var(--el-fill-color-light);
    transition: border-color 0.25s ease, background-color 0.25s ease;
    &.is-error {
        border-color: var(--el-color-danger-light-5);
        background-color: var(--el-color-danger-light-9);
        .status-icon {
            background-color: var(--el-color-danger-light-8);
            color: var(--el-color-danger);
        }
    }
}
.status-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    flex-shrink: 0;
    border-radius: 10px;
    font-size: 20px;
    background-color: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
}
.status-main {
    flex: 1;
    min-width: 0;
}
.status-headline {
    font-size: 14px;
    font-weight: 600;
    line-height: 22px;
    color: var(--el-text-color-primary);
}
.is-error .status-headline {
    color: var(--el-color-danger);
}
.status-sub {
    margin-top: 2px;
    font-size: 12.5px;
    line-height: 19px;
    color: var(--el-text-color-secondary);
    word-break: break-all;
    font-variant-numeric: tabular-nums;
}
.status-percent {
    flex-shrink: 0;
    align-self: center;
    font-size: 22px;
    font-weight: 700;
    line-height: 1;
    color: var(--el-color-primary);
    font-variant-numeric: tabular-nums;
}

/* ---------- mirror chips ---------- */
.mirror-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 12px;
}
.mirror-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 240px;
    padding: 3px 10px;
    border-radius: 14px;
    font-size: 12px;
    line-height: 18px;
    border: 1px solid var(--el-border-color-lighter);
    background-color: var(--el-bg-color);
    color: var(--el-text-color-secondary);
    cursor: default;
    .chip-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .chip-icon {
        font-size: 12px;
    }
    .chip-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background-color: currentColor;
    }
    &.is-active {
        border-color: var(--el-color-primary-light-5);
        color: var(--el-color-primary);
        background-color: var(--el-color-primary-light-9);
    }
    &.is-success {
        border-color: var(--el-color-success-light-5);
        color: var(--el-color-success);
        background-color: var(--el-color-success-light-9);
    }
    &.is-failed {
        border-color: var(--el-color-danger-light-5);
        color: var(--el-color-danger);
        background-color: var(--el-color-danger-light-9);
    }
}

/* ---------- progress bar ---------- */
.progress-row {
    margin-top: 12px;
    :deep(.el-progress-bar__outer) {
        border-radius: 999px;
        background-color: var(--el-fill-color-dark);
    }
    :deep(.el-progress-bar__inner) {
        border-radius: 999px;
    }
}

/* ---------- terminal panel ---------- */
.log-panel {
    margin-top: 16px;
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid var(--el-border-color-lighter);
}
.log-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 7px 12px;
    background-color: #2d2d30;
    border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}
.log-file {
    display: flex;
    align-items: center;
    gap: 7px;
}
.traffic {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    &.red {
        background-color: #ff5f57;
    }
    &.yellow {
        background-color: #febc2e;
    }
    &.green {
        background-color: #28c840;
    }
}
.log-filename {
    margin-left: 6px;
    font-size: 12px;
    color: #9d9d9d;
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
}
.log-actions {
    display: flex;
    align-items: center;
    gap: 8px;
}
.log-count {
    font-size: 12px;
    color: #9d9d9d;
    font-variant-numeric: tabular-nums;
}
.log-header :deep(.el-button) {
    color: #9d9d9d;
    height: 22px;
    padding: 0 4px;
    font-size: 12px;
    &:hover,
    &.is-clicked {
        color: #e0e0e0;
    }
    .copy-icon {
        margin-right: 3px;
        font-size: 13px;
    }
}
.log-box {
    height: 190px;
    padding: 10px 14px;
    overflow-y: auto;
    background-color: #1e1e1e;
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: 12px;
    line-height: 21px;
}
.log-empty {
    color: #6a6a6a;
    font-style: italic;
}
.log-line {
    display: flex;
    gap: 10px;
    white-space: pre-wrap;
    word-break: break-all;
    color: #d4d4d4;
}
.log-time {
    flex-shrink: 0;
    color: #7d7d7d;
}
.log-level {
    flex-shrink: 0;
    width: 40px;
    font-weight: 700;
}
.log-msg {
    flex: 1;
}
.log-line.is-info .log-level {
    color: #6ea8fe;
}
.log-line.is-success {
    color: #7ee787;
    .log-level {
        color: #3fb950;
    }
}
.log-line.is-warn {
    color: #e3b341;
    .log-level {
        color: #d29922;
    }
}
.log-line.is-error {
    color: #ff7b72;
    .log-level {
        color: #f85149;
    }
}

/* ---------- footer ---------- */
.dialog-footer {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
}
.footer-tip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--el-text-color-secondary);
}
.footer-icon {
    font-size: 14px;
    color: var(--el-color-warning);
}
</style>
