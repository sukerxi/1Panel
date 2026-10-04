<template>
    <el-dialog
        v-model="visible"
        :title="$t('setting.upgradeProgress.title', { version: version })"
        width="560px"
        :close-on-click-modal="false"
        :close-on-press-escape="false"
        :show-close="progress.failed"
        @close="handleClose"
    >
        <div class="upgrade-progress">
            <el-steps :active="activeStep" :process-status="processStatus" align-center finish-status="success" simple>
                <el-step :title="$t('setting.upgradeProgress.stageDownload')" />
                <el-step :title="$t('setting.upgradeProgress.stageDecompress')" />
                <el-step :title="$t('setting.upgradeProgress.stageBackup')" />
                <el-step :title="$t('setting.upgradeProgress.stageInstall')" />
                <el-step :title="$t('setting.upgradeProgress.stageRestart')" />
            </el-steps>

            <template v-if="progress.stage === 'download' || progress.stage === 'preparing'">
                <div v-if="progress.mirrors && progress.mirrors.length" class="mirror-list">
                    <div v-for="item in progress.mirrors" :key="item.name" class="mirror-item">
                        <el-icon class="mirror-icon" :class="iconClass(item.status)">
                            <Loading v-if="item.status === 'probing' || item.status === 'downloading'" />
                            <CircleCheck v-else-if="item.status === 'success'" />
                            <CircleClose v-else-if="item.status === 'failed'" />
                            <Clock v-else />
                        </el-icon>
                        <span class="mirror-name">{{ mirrorName(item.name) }}</span>
                        <el-tag size="small" :type="tagType(item.status)" effect="plain">
                            {{ $t(`setting.upgradeProgress.mirror_${item.status}`) }}
                        </el-tag>
                        <el-tooltip v-if="item.status === 'failed' && item.detail" :content="item.detail" placement="top">
                            <span class="mirror-detail">{{ item.detail }}</span>
                        </el-tooltip>
                    </div>
                </div>

                <div class="download-block">
                    <el-progress
                        v-if="progress.total > 0"
                        :percentage="percent"
                        :stroke-width="16"
                        :text-inside="true"
                        striped
                        striped-flow
                    />
                    <el-progress v-else :percentage="100" :stroke-width="16" :show-text="false" indeterminate :duration="3" />
                    <div class="download-meta">
                        <span>
                            {{ formatBytes(progress.downloaded) }}<template v-if="progress.total > 0">
                                / {{ formatBytes(progress.total) }}</template
                            >
                        </span>
                        <span v-if="progress.speedBps > 0">{{ formatBytes(progress.speedBps) }}/s</span>
                    </div>
                </div>
            </template>

            <div v-else-if="!progress.failed" class="stage-block">
                <el-icon class="is-loading stage-icon"><Loading /></el-icon>
                <span>{{ $t(stageText) }}</span>
            </div>

            <el-alert
                v-if="progress.failed"
                class="failed-alert"
                type="error"
                :title="$t('setting.upgradeProgress.failed')"
                :description="progress.message"
                :closable="false"
                show-icon
            />
        </div>
        <template #footer>
            <el-button v-if="progress.failed" type="primary" @click="visible = false">
                {{ $t('commons.button.close') }}
            </el-button>
            <span v-else class="footer-tip">{{ $t('setting.upgradeProgress.pleaseWait') }}</span>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { getUpgradeProgress } from '@/api/modules/setting';
import { Setting } from '@/api/interface/setting';
import { useGlobalStore } from '@/composables/useGlobalStore';
import { CircleCheck, CircleClose, Clock, Loading } from '@element-plus/icons-vue';
import { computed, onBeforeUnmount, ref } from 'vue';

const { isLoading, isOnRestart } = useGlobalStore();

const STAGES = ['download', 'decompress', 'backup', 'install', 'restart'];

const visible = ref(false);
const version = ref('');
const progress = ref<Setting.UpgradeProgress>({
    running: false,
    failed: false,
    stage: 'preparing',
    version: '',
    message: '',
    mirrors: [],
    downloaded: 0,
    total: 0,
    speedBps: 0,
});

let pollTimer: ReturnType<typeof setInterval> | null = null;

const percent = computed(() => {
    if (!progress.value.total || progress.value.total <= 0) return 0;
    return Math.min(100, Math.floor((progress.value.downloaded / progress.value.total) * 100));
});

const activeStep = computed(() => {
    const idx = STAGES.indexOf(progress.value.stage);
    if (idx < 0) return 0;
    return idx;
});

const processStatus = computed(() => (progress.value.failed ? 'error' : 'process'));

const stageText = computed(() => {
    const key = `setting.upgradeProgress.stageText_${progress.value.stage}`;
    return key;
});

const mirrorName = (name: string) => {
    if (name === 'github.com') {
        return 'GitHub';
    }
    return name;
};

const iconClass = (status: string) => ({
    'is-loading': status === 'probing' || status === 'downloading',
    success: status === 'success',
    danger: status === 'failed',
});

const tagType = (status: string) => {
    switch (status) {
        case 'success':
            return 'success';
        case 'failed':
            return 'danger';
        case 'downloading':
            return 'success';
        case 'probing':
            return 'primary';
        default:
            return 'info';
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

const start = (targetVersion: string) => {
    version.value = targetVersion;
    progress.value = {
        running: true,
        failed: false,
        stage: 'preparing',
        version: targetVersion,
        message: '',
        mirrors: [],
        downloaded: 0,
        total: 0,
        speedBps: 0,
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
});

defineExpose({
    start,
    resumeIfRunning,
});
</script>

<style lang="scss" scoped>
.upgrade-progress {
    padding: 8px 4px 0;
}
.mirror-list {
    margin-top: 22px;
}
.mirror-item {
    display: flex;
    align-items: center;
    height: 30px;
    gap: 8px;
    font-size: 13px;
}
.mirror-icon {
    font-size: 15px;
    color: var(--el-text-color-secondary);
    &.success {
        color: var(--el-color-success);
    }
    &.danger {
        color: var(--el-color-danger);
    }
}
.mirror-name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.mirror-detail {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-color-danger);
    font-size: 12px;
}
.download-block {
    margin-top: 22px;
}
.download-meta {
    display: flex;
    justify-content: space-between;
    margin-top: 10px;
    font-size: 13px;
    color: var(--el-text-color-regular);
}
.stage-block {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 26px;
    font-size: 14px;
}
.stage-icon {
    font-size: 18px;
    color: var(--el-color-primary);
}
.failed-alert {
    margin-top: 20px;
}
.footer-tip {
    font-size: 13px;
    color: var(--el-text-color-secondary);
}
</style>
