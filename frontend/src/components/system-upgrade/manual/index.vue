<template>
    <el-dialog
        v-model="visible"
        :title="$t('setting.manualUpgrade.title')"
        width="600px"
        :close-on-click-modal="false"
        :close-on-press-escape="false"
        @close="handleClose"
    >
        <el-alert :title="$t('setting.manualUpgrade.tip')" type="info" :closable="false" show-icon />
        <div class="release-link">
            <span>{{ $t('setting.manualUpgrade.releaseHint') }}</span>
            <el-link type="primary" :underline="false" @click="openReleases">{{ releaseUrl }}</el-link>
        </div>

        <el-upload
            ref="uploadRef"
            drag
            :auto-upload="false"
            :limit="1"
            accept=".tar.gz"
            :on-change="onFileChange"
            :on-exceed="onExceed"
            :on-remove="onRemove"
            :disabled="uploading"
        >
            <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
            <div class="el-upload__text">{{ $t('setting.manualUpgrade.dragText') }}</div>
            <template #tip>
                <div class="upload-tip">{{ $t('setting.manualUpgrade.nameRule') }}</div>
            </template>
        </el-upload>

        <div v-if="selectedFile" class="file-meta">
            <el-icon class="file-icon"><Document /></el-icon>
            <span class="file-name" :title="selectedFile.name">{{ selectedFile.name }}</span>
            <span class="file-size">{{ formatSize(selectedFile.size) }}</span>
        </div>
        <el-alert v-if="fileError" class="file-error" :title="fileError" type="error" :closable="false" show-icon />

        <el-progress
            v-if="uploading || uploadPercent === 100"
            class="upload-progress"
            :percentage="uploadPercent"
            :stroke-width="14"
            :text-inside="true"
            striped
            striped-flow
        />
        <div v-if="uploading && uploadPercent === 100" class="upload-tip upload-verifying">
            <el-icon class="is-loading"><Loading /></el-icon>
            {{ $t('setting.manualUpgrade.verifying') }}
        </div>

        <template #footer>
            <el-button @click="visible = false" :disabled="uploading">{{ $t('commons.button.cancel') }}</el-button>
            <el-button type="primary" :loading="uploading" :disabled="!selectedFile || !!fileError" @click="onSubmit">
                {{ uploading ? $t('setting.manualUpgrade.uploading') : $t('setting.manualUpgrade.submit') }}
            </el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { uploadUpgradePackage, upgrade } from '@/api/modules/setting';
import { Setting } from '@/api/interface/setting';
import { TimeoutEnum } from '@/enums/http-enum';
import i18n from '@/lang';
import { MsgSuccess } from '@/utils/message';
import { Document, Loading, UploadFilled } from '@element-plus/icons-vue';
import { ElMessageBox, genFileId, type UploadFile, type UploadInstance, type UploadRawFile } from 'element-plus';
import { ref } from 'vue';

const releaseUrl = 'https://github.com/sukerxi/1Panel/releases';
const packageNameReg = /^1panel-.+-linux-(amd64|arm64|armv7|ppc64le|s390x|riscv64)\.tar\.gz$/;

const emit = defineEmits(['upgrading', 'search']);

const visible = ref(false);
const uploading = ref(false);
const uploadPercent = ref(0);
const selectedFile = ref<UploadRawFile | null>(null);
const fileError = ref('');
const uploadRef = ref<UploadInstance>();

const acceptParams = () => {
    resetState();
    visible.value = true;
};

const resetState = () => {
    uploading.value = false;
    uploadPercent.value = 0;
    selectedFile.value = null;
    fileError.value = '';
    uploadRef.value?.clearFiles();
};

const handleClose = () => {
    if (uploading.value) return;
    visible.value = false;
};

const validateFile = (file: UploadRawFile): string => {
    if (!file.name.endsWith('.tar.gz')) {
        return i18n.global.t('setting.manualUpgrade.errFormat');
    }
    if (!packageNameReg.test(file.name)) {
        return i18n.global.t('setting.manualUpgrade.errName');
    }
    return '';
};

const onFileChange = (file: UploadFile) => {
    if (!file.raw) return;
    fileError.value = validateFile(file.raw);
    selectedFile.value = fileError.value ? null : file.raw;
};

const onExceed = (files: File[]) => {
    uploadRef.value?.clearFiles();
    const file = files[0] as UploadRawFile;
    file.uid = genFileId();
    uploadRef.value?.handleStart(file);
};

const onRemove = () => {
    selectedFile.value = null;
    fileError.value = '';
};

const formatSize = (size: number) => {
    if (!size) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const exponent = Math.min(Math.floor(Math.log(size) / Math.log(1024)), units.length - 1);
    return `${(size / Math.pow(1024, exponent)).toFixed(1)} ${units[exponent]}`;
};

const openReleases = () => {
    window.open(releaseUrl, '_blank', 'noopener,noreferrer');
};

const onSubmit = () => {
    if (!selectedFile.value || fileError.value) return;
    ElMessageBox.confirm(i18n.global.t('setting.upgradeHelper'), {
        confirmButtonText: i18n.global.t('commons.button.confirm'),
        cancelButtonText: i18n.global.t('commons.button.cancel'),
        type: 'info',
    }).then(async () => {
        const formData = new FormData();
        formData.append('file', selectedFile.value as UploadRawFile);
        uploading.value = true;
        uploadPercent.value = 0;
        let packageInfo: Setting.ManualPackageInfo | null = null;
        try {
            const res = await uploadUpgradePackage(formData, {
                timeout: TimeoutEnum.T_10M,
                onUploadProgress: (event) => {
                    if (event.total) {
                        uploadPercent.value = Math.min(99, Math.round((event.loaded / event.total) * 100));
                    }
                },
            });
            packageInfo = res.data;
            uploadPercent.value = 100;
        } catch {
            uploading.value = false;
            return;
        }
        try {
            await upgrade(packageInfo.version, packageInfo.package);
        } catch {
            uploading.value = false;
            return;
        }
        MsgSuccess(i18n.global.t('commons.msg.operationSuccess'));
        visible.value = false;
        emit('upgrading', packageInfo.version, true);
        emit('search');
    });
};

defineExpose({
    acceptParams,
});
</script>

<style lang="scss" scoped>
.release-link {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    margin: 12px 0;
    font-size: 13px;
    color: var(--el-text-color-regular);
}
.upload-tip {
    margin-top: 6px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.file-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    padding: 8px 12px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 4px;
    font-size: 13px;
}
.file-icon {
    color: var(--el-color-primary);
}
.file-name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.file-size {
    color: var(--el-text-color-secondary);
}
.file-error {
    margin-top: 10px;
}
.upload-progress {
    margin-top: 14px;
}
.upload-verifying {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    margin-top: 8px;
    color: var(--el-color-primary);
}
</style>
