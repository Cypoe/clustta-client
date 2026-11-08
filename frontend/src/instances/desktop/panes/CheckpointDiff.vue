<template>
  <div class="general-pane-root checkpoint-diff-root">
    <PageState v-if="!selectedId" :message="$t('panes.diffSelectCheckpoint')" :illustration="'/page-states/resources.png'" />

    <div v-else-if="loading" class="diff-loading">{{ $t('common.loading') }}</div>

    <div v-else-if="error" class="diff-error">{{ error }}</div>

    <template v-else-if="diff">
      <div class="diff-header">
        <div class="diff-pair">
          <DropDownBox :items="previousOptions" :selectedItem="previousLabel" :onSelect="selectPrevious"
            :fixedWidth="true" :placeHolder="$t('panes.diffPrevious')" />
          <span class="diff-pair-arrow">→</span>
          <span class="diff-current" v-tooltip="selectedCp?.comment">{{ selectedCp?.comment || shortId(selectedId) }}</span>
        </div>
        <div class="diff-meta">
          <span class="diff-badge" :class="'diff-badge-' + diff.kind">{{ diff.kind }}</span>
          <span v-if="diff.a?.eol || diff.b?.eol" class="diff-badge diff-badge-eol">
            {{ diff.a?.eol || '?' }} → {{ diff.b?.eol || '?' }}
          </span>
          <span v-if="eolShiftOnly" class="diff-hint">all chunks differ at byte level (line endings)</span>
          <span class="diff-stat">{{ diff.chunks_shared }}/{{ diff.chunks_total }} {{ $t('panes.diffChunksShared') }}</span>
          <span class="diff-stat">{{ formatBytes(diff.bytes_shared) }}</span>
          <span v-if="diff.truncated" class="diff-badge diff-badge-truncated">truncated</span>
        </div>
      </div>

      <div class="diff-body">
        <div class="diff-scroll app-scrollbar">
        <div v-if="diff.kind === 'identical'" class="empty-text">{{ $t('panes.diffIdentical') }}</div>
        <div v-else-if="diff.kind === 'text' && !diff.line_hunks?.length" class="empty-text">
          {{ $t('panes.diffNoLineChanges') }}
        </div>

        <div v-if="diff.line_hunks?.length" class="diff-hunks">
          <div v-for="(hunk, hi) in diff.line_hunks" :key="hi" class="diff-hunk">
            <div class="diff-hunk-header">@@ -{{ hunk.a_start }} +{{ hunk.b_start }} @@</div>
            <div v-for="(e, ei) in hunk.entries" :key="ei" class="diff-line" :class="'diff-line-' + e.kind">
              <span class="diff-lineno">{{ e.a_line > 0 ? e.a_line : '' }}</span>
              <span class="diff-lineno">{{ e.b_line > 0 ? e.b_line : '' }}</span>
              <span class="diff-marker">{{ markerFor(e.kind) }}</span>
              <span class="diff-text">{{ e.text }}</span>
            </div>
          </div>
        </div>

        <div v-if="diff.byte_spans?.length" class="diff-spans">
          <div v-for="(s, i) in diff.byte_spans" :key="i" class="diff-span">
            <span class="diff-span-off">@{{ s.a_off }}..{{ s.a_off + s.a_len }}</span>
            <span class="diff-span-hex diff-line-del">{{ s.a_hex }}</span>
            <span class="diff-span-arrow">→</span>
            <span class="diff-span-hex diff-line-add">{{ s.b_hex }}</span>
          </div>
        </div>
        </div>
        <div class="diff-minimap">
          <div v-if="diff.regions?.length" class="mm-track mm-chunks" v-tooltip="'chunk storage: shared vs changed'">
            <div v-for="(r, i) in diff.regions" :key="i" class="diff-region"
              :class="r.shared ? 'diff-region-shared' : 'diff-region-changed'"
              :style="{ flexGrow: Math.max(r.a.length, r.b.length) }" />
          </div>
          <div v-if="lineMarkers.length" class="mm-track mm-lines" v-tooltip="'line changes in the file'">
            <div v-for="(m, i) in lineMarkers" :key="i" class="mm-marker"
              :class="'mm-marker-' + m.kind" :style="{ top: m.pos + '%' }" />
          </div>
        </div>
      </div>
    </template>

    <PageState v-else :message="$t('panes.diffSelectCheckpoint')" :illustration="'/page-states/resources.png'" />
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { CheckpointService } from '@/services';
import DropDownBox from '@/instances/common/components/DropDownBox.vue';
import PageState from '@/instances/common/components/PageState.vue';
import { useAssetStore } from '@/stores/assets';
import { useNotificationStore } from '@/stores/notifications';
import { useProjectStore } from '@/stores/projects';
import { useTrayStates } from '@/stores/TrayStates';
import utils from '@/services/utils';

const { t } = useI18n();
const assetStore = useAssetStore();
const notificationStore = useNotificationStore();
const projectStore = useProjectStore();
const trayStates = useTrayStates();

const checkpoints = ref([]);
const diff = ref(null);

// Vertical positions of changed lines in file B, for the minimap column.
// Deleted entries carry no b_line, so they anchor at the running b position.
const lineMarkers = computed(() => {
  const d = diff.value;
  if (!d) return [];
  const markers = [];
  if (d.line_hunks?.length && d.lines_b) {
    for (const h of d.line_hunks) {
      let bLine = h.b_start;
      for (const e of h.entries) {
        if (e.b_line) bLine = e.b_line;
        if (e.kind === 'del' || e.kind === 'add') {
          markers.push({ kind: e.kind, pos: ((bLine - 1) / d.lines_b) * 100 });
        }
      }
    }
  } else if (d.byte_spans?.length && d.b?.file_size) {
    for (const s of d.byte_spans) {
      markers.push({ kind: 'del', pos: (s.b_off / d.b.file_size) * 100 });
    }
  }
  return markers;
});

// True when every chunk differs only because line endings shifted the bytes.
const eolShiftOnly = computed(() =>
  diff.value && diff.value.a?.eol !== diff.value.b?.eol && diff.value.chunks_shared === 0
);
const error = ref('');
const loading = ref(false);
const previousId = ref('');

// trayStates.diffContext: { assetId, checkpointId } — set when a checkpoint is
// expanded or its diff action is clicked.
const selectedId = computed(() => trayStates.diffContext?.checkpointId || '');
const assetId = computed(() => trayStates.diffContext?.assetId || '');

const selectedCp = computed(() => checkpoints.value.find(c => c.id === selectedId.value));
const previousCp = computed(() => checkpoints.value.find(c => c.id === previousId.value));

const labelFor = (cp) => `${cp.comment || cp.id.slice(0, 8)} — ${utils.formatDate(cp.created_at)}`;
const previousLabel = computed(() => previousCp.value ? labelFor(previousCp.value) : '');
const previousOptions = computed(() => checkpoints.value.filter(c => c.id !== selectedId.value).map(labelFor));
const shortId = (id) => (id || '').slice(0, 8);

const markerFor = (kind) => ({ add: '+', del: '−', context: ' ' }[kind] || ' ');
const formatBytes = (n) => {
  if (!n) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB'];
  let i = 0, v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i ? 1 : 0)} ${units[i]}`;
};

const selectPrevious = (label) => {
  const cp = checkpoints.value.find(c => labelFor(c) === label);
  if (cp) previousId.value = cp.id;
};

const runDiff = async () => {
  if (!selectedId.value || !previousId.value || selectedId.value === previousId.value) {
    diff.value = null;
    return;
  }
  loading.value = true;
  error.value = '';
  try {
    diff.value = await CheckpointService.DiffCheckpoints(
      projectStore.activeProject.uri, previousId.value, selectedId.value);
  } catch (e) {
    error.value = e.message || String(e);
    diff.value = null;
  } finally {
    loading.value = false;
  }
};

// The list is created_at DESC — the entry after the selected one is its
// predecessor, the default "before" side.
const defaultPrevious = () => {
  const idx = checkpoints.value.findIndex(c => c.id === selectedId.value);
  previousId.value = idx >= 0 ? (checkpoints.value[idx + 1]?.id || '') : '';
};

watch([selectedId, assetId], async () => {
  if (!assetId.value) { checkpoints.value = []; diff.value = null; return; }
  try {
    checkpoints.value = await CheckpointService.GetCheckpoints(projectStore.activeProject.uri, assetId.value);
  } catch (e) {
    notificationStore.errorNotification(t('panes.diffLoadFailed'), e);
    return;
  }
  defaultPrevious();
}, { immediate: true });

watch(previousId, runDiff);
watch(selectedId, runDiff);

// Selecting a different asset must not leave a stale diff on screen.
watch(() => assetStore.selectedAsset?.id, (id) => {
  if (id && trayStates.diffContext?.assetId && id !== trayStates.diffContext.assetId) {
    trayStates.diffContext = null;
    diff.value = null;
  }
});
</script>

<style scoped>
@import "@/assets/desktop.css";

.checkpoint-diff-root {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  flex: 1;
  min-height: 0;
  height: auto;
  width: 100%;
  align-self: stretch;
}
.diff-loading, .diff-error { padding: 1rem; font-size: 12px; }
.diff-error { color: var(--error, #e06c75); }
.diff-header {
  display: flex;
  flex-direction: column;
  flex: none;
  width: 100%;
  box-sizing: border-box;
  gap: .5rem;
  padding: .5rem .75rem;
  border-bottom: 1px solid var(--border, rgba(255,255,255,.08));
}
.diff-pair {
  display: flex;
  align-items: center;
  gap: .5rem;
  font-size: 12px;
  min-width: 0;
}
.diff-pair-arrow { color: var(--text-muted); }
.diff-current {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}
.diff-meta {
  display: flex;
  flex-wrap: wrap;
  gap: .4rem;
  align-items: center;
  font-size: 11px;
  color: var(--text-muted);
}
.diff-badge {
  padding: .1rem .5rem;
  border-radius: 999px;
  border: 1px solid var(--border-strong, rgba(255,255,255,.15));
  font-size: 10px;
  text-transform: uppercase;
}
.diff-badge-text { color: var(--text); }
.diff-badge-binary { color: var(--warning, #d19a66); }
.diff-badge-identical { color: var(--success, #98c379); }
.diff-badge-eol { text-transform: none; }
.diff-badge-truncated { color: var(--warning, #d19a66); }
.diff-body {
  flex: 1;
  min-height: 0;
  width: 100%;
  display: flex;
  overflow: hidden;
}
.diff-scroll {
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: auto;
}
.diff-minimap {
  flex: none;
  display: flex;
  gap: 2px;
  padding: 2px 1px;
}
.mm-track {
  width: 6px;
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.mm-lines { display: block; }
.mm-marker {
  position: absolute;
  left: 0;
  right: 0;
  height: 2px;
  border-radius: 1px;
}
.mm-marker-del { background: var(--error, #e06c75); }
.mm-marker-add { background: var(--success, #98c379); }
.diff-hint { font-size: 10px; color: var(--text-muted); font-style: italic; }
.diff-region { border-radius: 2px; min-height: 3px; }
.diff-region-shared { background: var(--success, #98c379); opacity: .5; }
.diff-region-changed { background: var(--error, #e06c75); }
.empty-text { color: var(--text-muted); padding: 1.5rem; text-align: center; font-size: 12px; }
.diff-hunks {
  font-family: var(--font-family-mono, monospace);
  font-size: 11px;
  display: flex;
  flex-direction: column;
  gap: .6rem;
  padding: .5rem 0;
}
.diff-hunk-header {
  color: var(--text-muted);
  padding: .1rem .5rem;
  position: sticky;
  left: 0;
}
.diff-line {
  display: flex;
  white-space: pre;
  line-height: 1.45;
  min-width: min-content;
}
.diff-lineno {
  width: 4ch;
  min-width: 4ch;
  text-align: right;
  color: var(--text-muted);
  opacity: .6;
  margin-right: .6ch;
  user-select: none;
}
.diff-marker { width: 1.2ch; min-width: 1.2ch; user-select: none; }
.diff-line-add { background: rgba(152, 195, 121, .12); color: #98c379; }
.diff-line-del { background: rgba(224, 108, 117, .12); color: #e06c75; }
.diff-line-context { color: var(--text-muted); }
.diff-spans {
  font-family: var(--font-family-mono, monospace);
  font-size: 11px;
  display: flex;
  flex-direction: column;
  gap: .3rem;
  padding: .5rem;
}
.diff-span { display: flex; gap: .8rem; align-items: center; }
.diff-span-off { color: var(--text-muted); }
.diff-span-hex { padding: 0 .3rem; border-radius: 3px; }
</style>
