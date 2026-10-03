<template>
    <DrawerPro v-model="open" :header="$t('file.batchRename.title')" @close="handleClose" size="large">
        <div v-loading="loading" class="batch-rename">
            <el-alert
                :title="$t('file.batchRename.sameDirTip')"
                type="info"
                :closable="false"
                class="mb-2"
            />
            <el-row :gutter="16">
                <el-col :span="9">
                    <el-form label-position="top" :model="rule">
                        <el-form-item :label="$t('file.batchRename.ruleType')">
                            <el-radio-group v-model="rule.type" @change="refreshPreview">
                                <el-radio-button value="replace">{{ $t('file.batchRename.types.replace') }}</el-radio-button>
                                <el-radio-button value="insert">{{ $t('file.batchRename.types.insert') }}</el-radio-button>
                                <el-radio-button value="case">{{ $t('file.batchRename.types.case') }}</el-radio-button>
                                <el-radio-button value="number">{{ $t('file.batchRename.types.number') }}</el-radio-button>
                                <el-radio-button value="episode">{{ $t('file.batchRename.types.episode') }}</el-radio-button>
                            </el-radio-group>
                        </el-form-item>

                        <template v-if="rule.type === 'replace'">
                            <el-form-item :label="$t('file.batchRename.find')">
                                <el-input v-model="rule.find" @input="schedulePreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.replaceWith')">
                                <el-input v-model="rule.replace" @input="schedulePreview" />
                            </el-form-item>
                            <el-form-item>
                                <el-checkbox v-model="rule.useRegex" @change="refreshPreview">
                                    {{ $t('file.batchRename.useRegex') }}
                                </el-checkbox>
                                <el-checkbox v-model="rule.matchCase" @change="refreshPreview">
                                    {{ $t('file.batchRename.matchCase') }}
                                </el-checkbox>
                            </el-form-item>
                            <div class="tips">{{ $t('file.batchRename.regexTip') }}</div>
                        </template>

                        <template v-if="rule.type === 'insert'">
                            <el-form-item :label="$t('file.batchRename.insertText')">
                                <el-input v-model="rule.text" @input="schedulePreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.insertPosition')">
                                <el-input-number v-model="rule.position" :min="-1" :step="1" @change="refreshPreview" />
                                <span class="ml-2 tips">{{ $t('file.batchRename.positionTip') }}</span>
                            </el-form-item>
                        </template>

                        <template v-if="rule.type === 'case'">
                            <el-form-item :label="$t('file.batchRename.caseType')">
                                <el-radio-group v-model="rule.caseType" @change="refreshPreview">
                                    <el-radio value="lower">{{ $t('file.batchRename.caseLower') }}</el-radio>
                                    <el-radio value="upper">{{ $t('file.batchRename.caseUpper') }}</el-radio>
                                    <el-radio value="title">{{ $t('file.batchRename.caseTitle') }}</el-radio>
                                </el-radio-group>
                            </el-form-item>
                        </template>

                        <template v-if="rule.type === 'number'">
                            <el-form-item :label="$t('file.batchRename.start')">
                                <el-input-number v-model="rule.start" :step="1" @change="refreshPreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.step')">
                                <el-input-number v-model="rule.step" :step="1" :min="1" @change="refreshPreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.padding')">
                                <el-input-number v-model="rule.padding" :min="0" :max="10" :step="1" @change="refreshPreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.template')">
                                <el-input v-model="rule.template" :placeholder="'{name}_{n}'" @input="schedulePreview" />
                            </el-form-item>
                            <div class="tips">{{ $t('file.batchRename.numberTip') }}</div>
                        </template>

                        <template v-if="rule.type === 'episode'">
                            <el-form-item :label="$t('file.batchRename.seriesName')">
                                <el-input v-model="rule.text" @input="schedulePreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.offset')">
                                <el-input-number v-model="rule.offset" :step="1" @change="refreshPreview" />
                                <span class="ml-2 tips">{{ $t('file.batchRename.offsetTip') }}</span>
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.padding')">
                                <el-input-number v-model="rule.padding" :min="0" :max="10" :step="1" @change="refreshPreview" />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.customPattern')">
                                <el-input
                                    v-model="rule.pattern"
                                    :placeholder="$t('file.batchRename.customPatternPlaceholder')"
                                    @input="schedulePreview"
                                />
                            </el-form-item>
                            <el-form-item :label="$t('file.batchRename.template')">
                                <el-input v-model="rule.template" :placeholder="'{text} - E{episode}'" @input="schedulePreview" />
                            </el-form-item>
                            <div class="tips">{{ $t('file.batchRename.episodeTip') }}</div>
                        </template>
                    </el-form>
                </el-col>
                <el-col :span="15">
                    <div class="preview-header">
                        <span>{{ $t('file.batchRename.preview') }}</span>
                        <span class="preview-summary">
                            {{ $t('file.batchRename.changedCount', { count: changedCount }) }}
                            <el-tag v-if="errorCount > 0" type="danger" class="ml-2">
                                {{ $t('file.batchRename.errorCount', { count: errorCount }) }}
                            </el-tag>
                        </span>
                    </div>
                    <el-table :data="previewItems" height="520" size="small" class="preview-table">
                        <el-table-column type="index" width="48" />
                        <el-table-column :label="$t('file.batchRename.oldName')" prop="oldName" show-overflow-tooltip />
                        <el-table-column width="40" align="center">
                            <template #default>
                                <span>-&gt;</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="$t('file.batchRename.newName')" show-overflow-tooltip>
                            <template #default="scope">
                                <el-tooltip
                                    v-if="scope.row.error"
                                    :content="scope.row.error"
                                    placement="top"
                                >
                                    <span class="error-name">{{ scope.row.newName || scope.row.oldName }}</span>
                                </el-tooltip>
                                <span v-else-if="!scope.row.changed" class="unchanged-name">{{ scope.row.newName }}</span>
                                <span v-else class="new-name">{{ scope.row.newName }}</span>
                            </template>
                        </el-table-column>
                    </el-table>
                </el-col>
            </el-row>
        </div>
        <template #footer>
            <span class="dialog-footer">
                <el-button @click="handleClose">{{ $t('commons.button.cancel') }}</el-button>
                <el-button type="primary" :loading="submitting" :disabled="!canSubmit" @click="submit()">
                    {{ $t('commons.button.confirm') }}
                </el-button>
            </span>
        </template>
    </DrawerPro>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { File } from '@/api/interface/file';
import { batchRename, batchRenamePreview } from '@/api/modules/files';
import i18n from '@/lang';
import { MsgError, MsgSuccess } from '@/utils/message';

interface BatchRenameProps {
    files: File.File[];
}

const open = ref(false);
const loading = ref(false);
const submitting = ref(false);
const paths = ref<string[]>([]);
const previewItems = ref<File.BatchRenameItem[]>([]);
let previewTimer: ReturnType<typeof setTimeout> | null = null;

const rule = reactive<File.BatchRenameRule>({
    type: 'episode',
    find: '',
    replace: '',
    useRegex: false,
    matchCase: false,
    position: -1,
    text: '',
    caseType: 'lower',
    start: 1,
    step: 1,
    padding: 2,
    template: '',
    pattern: '',
    offset: 0,
});

const changedCount = computed(() => previewItems.value.filter((item) => item.changed && !item.error).length);
const errorCount = computed(() => previewItems.value.filter((item) => item.error).length);
const canSubmit = computed(() => changedCount.value > 0 && errorCount.value === 0);

const em = defineEmits(['close']);
const handleClose = () => {
    open.value = false;
    em('close', false);
};

const acceptParams = (props: BatchRenameProps) => {
    paths.value = props.files.map((file) => file.path);
    open.value = true;
    refreshPreview();
};

const buildRequest = (): File.FileBatchRenameReq => ({
    paths: paths.value,
    rule: { ...rule },
});

const refreshPreview = async () => {
    if (!open.value || paths.value.length === 0) {
        return;
    }
    loading.value = true;
    try {
        const res = await batchRenamePreview(buildRequest());
        previewItems.value = res.data || [];
    } catch (error) {
        previewItems.value = [];
        console.error('batch rename preview failed:', error);
    } finally {
        loading.value = false;
    }
};

const schedulePreview = () => {
    if (previewTimer) {
        clearTimeout(previewTimer);
    }
    previewTimer = setTimeout(() => {
        refreshPreview();
    }, 300);
};

const submit = async () => {
    if (!canSubmit.value) {
        MsgError(i18n.global.t('file.batchRename.hasErrors'));
        return;
    }
    submitting.value = true;
    try {
        await batchRename(buildRequest());
        MsgSuccess(i18n.global.t('commons.msg.operationSuccess'));
        handleClose();
    } finally {
        submitting.value = false;
    }
};

defineExpose({ acceptParams });
</script>

<style lang="scss" scoped>
.batch-rename {
    .tips {
        font-size: 12px;
        color: var(--el-text-color-secondary);
        line-height: 1.6;
    }
    .preview-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 8px;
        font-weight: 600;
    }
    .preview-summary {
        font-size: 12px;
        font-weight: 400;
        color: var(--el-text-color-secondary);
    }
    .error-name {
        color: var(--el-color-danger);
        text-decoration: line-through;
        cursor: not-allowed;
    }
    .unchanged-name {
        color: var(--el-text-color-secondary);
    }
    .new-name {
        color: var(--el-color-success);
    }
}
</style>
