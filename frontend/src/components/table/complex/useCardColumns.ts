import { computed, defineComponent, h, type Component, type PropType, type VNode } from 'vue';

import FuTableOperations from '@/components/table/TableOperations.vue';
import { getTableColumnKey, isElTableColumnVNode } from '@/components/table/shared';

export type CardType = 'name' | 'status' | 'content' | 'content-full' | 'description' | 'button';

const cardTypes: CardType[] = ['name', 'status', 'content', 'content-full', 'description', 'button'];

const isOperationsColumn = (column: VNode) => {
    const type = column.type as any;
    return (
        type === FuTableOperations ||
        type?.name === 'FuTableOperations' ||
        type?.__name === 'FuTableOperations' ||
        type === 'fu-table-operations' ||
        type === 'FuTableOperations'
    );
};

const CardColumnValue = defineComponent({
    name: 'ComplexTableCardColumnValue',
    props: {
        column: { type: Object as PropType<VNode>, required: true },
        row: { type: Object, required: true },
        index: { type: Number, required: true },
    },
    setup(props) {
        return () => {
            const columnProps = (props.column.props || {}) as Record<string, any>;
            const scope = { row: props.row, $index: props.index, column: columnProps, viewMode: 'card' };
            let content: any;
            if (isOperationsColumn(props.column)) {
                content = h(FuTableOperations, { ...columnProps, cardRow: props.row });
            } else {
                const defaultSlot = (props.column.children as any)?.default;
                if (typeof defaultSlot === 'function') {
                    content = defaultSlot(scope);
                } else {
                    const value = String(columnProps.prop || '')
                        .split('.')
                        .filter(Boolean)
                        .reduce((current, key) => current?.[key], props.row as any);
                    content =
                        typeof columnProps.formatter === 'function'
                            ? columnProps.formatter(props.row, columnProps, value, props.index)
                            : (value ?? '-');
                }
            }
            return h('span', { class: 'complex-table__card-column-value' }, content);
        };
    },
});

export const useCardColumns = (columnNodes: () => VNode[], columns: () => unknown) => {
    const isVisible = (column: VNode) => {
        if (isOperationsColumn(column)) {
            return true;
        }
        const configuredColumns = columns();
        if (!Array.isArray(configuredColumns) || configuredColumns.length === 0) {
            return true;
        }
        const config = configuredColumns.find((item: any) => item.key === getTableColumnKey(column));
        return config?.show !== false;
    };

    const cardColumns = computed<Record<CardType, VNode[]>>(() => {
        const grouped = Object.fromEntries(cardTypes.map((type) => [type, []])) as Record<CardType, VNode[]>;
        const untagged: VNode[] = [];
        for (const column of columnNodes().filter(
            (vnode) => isElTableColumnVNode(vnode) || isOperationsColumn(vnode),
        )) {
            if (!isVisible(column)) {
                continue;
            }
            // 行操作列统一归入卡片按钮区，无需显式标注 card-type
            if (isOperationsColumn(column)) {
                grouped.button.push(column);
                continue;
            }
            const columnProps = (column.props || {}) as Record<string, any>;
            // 选择框、序号、展开列为功能性列，卡片中不直接展示
            if (['selection', 'index', 'expand'].includes(columnProps.type)) {
                continue;
            }
            const cardType = (columnProps.cardType || columnProps['card-type']) as CardType | undefined;
            if (cardType && cardTypes.includes(cardType)) {
                grouped[cardType].push(column);
            } else {
                untagged.push(column);
            }
        }
        // 未标注 card-type 的列自动兜底：第一列作卡片标题，其余作描述行，
        // 使所有列表页在移动端卡片视图下都能完整展示信息
        if (untagged.length) {
            if (grouped.name.length === 0) {
                grouped.name.push(untagged.shift() as VNode);
            }
            grouped.description.push(...untagged);
        }
        return grouped;
    });

    return {
        CardColumnValue: CardColumnValue as Component,
        cardColumns,
        getColumnKey: getTableColumnKey,
        getColumnLabel: (column: VNode) => String((column.props as Record<string, any> | null)?.label || ''),
    };
};
