<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { http } from '@/api/http'
import { formatDateTime } from '@/utils/format'

interface TaskRow {
  id: number
  task_id: string
  user_id: number
  user_email: string
  downstream_user_id: string
  downstream_username: string
  downstream_user_email: string
  downstream_user_label: string
  prompt: string
  n: number
  size: string
  upscale: string
  status: string
  result_urls_parsed: string[]
  error: string
  error_code?: string
  error_message?: string
  error_detail?: string
  credit_cost: number
  estimated_credit: number
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

const loading = ref(false)
const rows = ref<TaskRow[]>([])
const total = ref(0)
const filter = reactive({
  keyword: '',
  status: '',
  page: 1,
  page_size: 20,
})

async function fetchList() {
  loading.value = true
  try {
    const params: Record<string, any> = {
      page: filter.page,
      page_size: filter.page_size,
    }
    if (filter.keyword) params.keyword = filter.keyword
    if (filter.status) params.status = filter.status
    const d = await http.get<any, any>('/api/admin/image-tasks', { params })
    rows.value = d.list || []
    total.value = d.total || 0
  } finally {
    loading.value = false
  }
}

function onSearch() {
  filter.page = 1
  fetchList()
}
function onReset() {
  filter.keyword = ''
  filter.status = ''
  filter.page = 1
  fetchList()
}

// 弹窗预览图片
const previewDlg = ref(false)
const previewRow = ref<TaskRow | null>(null)
const largePreviewDlg = ref(false)
const largePreviewUrl = ref('')
const largePreviewIndex = ref(0)
function openPreview(row: TaskRow) {
  previewRow.value = row
  previewDlg.value = true
}
function openLargePreview(row: TaskRow | null, idx: number) {
  const url = row?.result_urls_parsed?.[idx]
  if (!row || !url) return
  previewRow.value = row
  largePreviewIndex.value = idx
  largePreviewUrl.value = url
  largePreviewDlg.value = true
}
function switchLargePreview(offset: number) {
  const urls = previewRow.value?.result_urls_parsed || []
  if (!urls.length) return
  const next = (largePreviewIndex.value + offset + urls.length) % urls.length
  largePreviewIndex.value = next
  largePreviewUrl.value = urls[next] || ''
}

const statusColor: Record<string, 'success' | 'danger' | 'warning' | 'info' | 'primary'> = {
  success: 'success',
  failed: 'danger',
  running: 'warning',
  queued: 'info',
  dispatched: 'info',
}

const errorCodeLabel: Record<string, string> = {
  content_moderation: '内容安全拒绝',
  invalid_request_error: '参数不被上游接受',
  upstream_error: '上游未出图',
  poll_timeout: '轮询超时',
  download_failed: '下载失败',
  no_available_account: '无可用账号',
  rate_limited: '上游限流',
  interrupted: '部署/重启中断',
}

const errorTagType: Record<string, 'danger' | 'warning' | 'info'> = {
  content_moderation: 'danger',
  invalid_request_error: 'warning',
  upstream_error: 'warning',
  poll_timeout: 'warning',
  interrupted: 'info',
}

function splitTaskError(error = '') {
  const trimmed = error.trim()
  if (!trimmed) return { code: '', detail: '' }
  const idx = trimmed.indexOf(':')
  if (idx > 0 && /^[a-zA-Z0-9_-]+$/.test(trimmed.slice(0, idx).trim())) {
    return { code: trimmed.slice(0, idx).trim(), detail: trimmed.slice(idx + 1).trim() }
  }
  if (trimmed.startsWith('upstream ')) return { code: 'upstream_error', detail: trimmed }
  return { code: trimmed, detail: '' }
}

function taskErrorCode(row: TaskRow) {
  return row.error_code || splitTaskError(row.error).code
}

function errorReason(row: TaskRow) {
  const code = taskErrorCode(row)
  return errorCodeLabel[code] || code || '-'
}

function errorDetail(row: TaskRow) {
  const parsed = splitTaskError(row.error)
  return row.error_message || row.error_detail || parsed.detail || row.error || ''
}

function errorType(row: TaskRow) {
  const code = taskErrorCode(row)
  return errorTagType[code] || 'warning'
}

function errorCopyText(row: TaskRow) {
  return [row.error_message, row.error_detail || row.error]
    .filter((v, idx, arr) => v && arr.indexOf(v) === idx)
    .join('\n')
}

async function copyError(row: TaskRow) {
  const text = errorCopyText(row)
  if (!text) return
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.left = '-9999px'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    ElMessage.success('失败原因已复制')
  } catch (e: any) {
    ElMessage.error(e?.message || '复制失败')
  }
}

onMounted(fetchList)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <h2 class="page-title" style="margin:0">生成记录</h2>
      <div style="color:var(--el-text-color-secondary);font-size:13px;margin:4px 0 14px">
        全站图片生成任务历史,含后端顾客、号池用户、提示词、生成结果与耗时。
      </div>

      <el-form inline class="flex-wrap-gap" @submit.prevent="onSearch">
        <el-input v-model="filter.keyword" placeholder="提示词 / 顾客 / 邮箱" clearable style="width:280px" />
        <el-select v-model="filter.status" placeholder="状态" clearable style="width:130px">
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
          <el-option label="运行中" value="running" />
          <el-option label="队列中" value="queued" />
        </el-select>
        <el-button type="primary" @click="onSearch"><el-icon><Search /></el-icon> 查询</el-button>
        <el-button @click="onReset">重置</el-button>
      </el-form>

      <el-table v-loading="loading" :data="rows" stripe style="margin-top:12px" size="small">
        <el-table-column prop="id" label="ID" width="72" />
        <el-table-column label="顾客 / 号池用户" min-width="220">
          <template #default="{ row }">
            <div v-if="row.downstream_user_label || row.downstream_username || row.downstream_user_email || row.downstream_user_id">
              <div>{{ row.downstream_user_label || row.downstream_username || row.downstream_user_email || '-' }}</div>
              <div style="font-size:11px;color:var(--el-text-color-secondary)">
                new-api uid {{ row.downstream_user_id || '-' }}
              </div>
            </div>
            <div style="font-size:11px;color:var(--el-text-color-secondary);margin-top:2px">
              号池 {{ row.user_email || '-' }} · uid {{ row.user_id }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="提示词" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">
            <span>{{ row.prompt || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="规格" width="110">
          <template #default="{ row }">
            <div>{{ row.size }}</div>
            <div v-if="row.upscale" style="font-size:11px;color:var(--el-color-success)">{{ row.upscale }}</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusColor[row.status] || 'info'" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="80">
          <template #default="{ row }">
            <el-button
              v-if="row.result_urls_parsed?.length"
              type="primary" link size="small"
              @click="openPreview(row)"
            >预览({{ row.result_urls_parsed.length }})</el-button>
            <span v-else-if="row.error || row.error_message" style="font-size:11px;color:var(--el-color-danger)" :title="errorDetail(row)">失败</span>
            <span v-else style="color:var(--el-text-color-secondary)">-</span>
          </template>
        </el-table-column>
        <el-table-column label="失败原因" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <div v-if="row.error || row.error_message" class="error-reason">
              <el-tag :type="errorType(row)" size="small">{{ errorReason(row) }}</el-tag>
              <span class="error-detail" :title="row.error_detail || row.error">{{ errorDetail(row) }}</span>
              <el-button type="primary" link size="small" @click="copyError(row)">复制</el-button>
            </div>
            <span v-else style="color:var(--el-text-color-secondary)">-</span>
          </template>
        </el-table-column>
        <el-table-column label="积分" width="100">
          <template #default="{ row }">
            <div>{{ row.credit_cost }}</div>
            <div style="font-size:11px;color:var(--el-text-color-secondary)">预估 {{ row.estimated_credit }}</div>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="完成时间" width="160">
          <template #default="{ row }">{{ row.finished_at ? formatDateTime(row.finished_at) : '-' }}</template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top:16px;justify-content:flex-end;display:flex"
        :current-page="filter.page"
        @current-change="(p: number) => { filter.page = p; fetchList() }"
        :page-size="filter.page_size"
        @size-change="(s: number) => { filter.page_size = s; filter.page = 1; fetchList() }"
        :total="total"
        :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next"
      />
    </div>

    <!-- 图片预览弹窗 -->
    <el-dialog v-model="previewDlg" title="生成结果预览" width="680px">
      <div v-if="previewRow">
        <div style="font-size:13px;color:var(--el-text-color-secondary);margin-bottom:10px;word-break:break-all">
          {{ previewRow.prompt }}
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:8px">
          <button
            v-for="(url, idx) in previewRow.result_urls_parsed"
            :key="idx"
            type="button"
            class="image-task-thumb-button"
            @click.stop.prevent="openLargePreview(previewRow, idx)"
          >
            <img
              :src="url"
              alt="生成结果缩略图"
              class="image-task-thumb"
              loading="lazy"
              @dragstart.prevent
            />
          </button>
        </div>
        <div v-if="previewRow.error" style="margin-top:12px;color:var(--el-color-danger);font-size:12px;word-break:break-all">
          错误:{{ previewRow.error }}
        </div>
      </div>
    </el-dialog>

    <el-dialog
      v-model="largePreviewDlg"
      title="查看生成结果"
      width="min(92vw, 1100px)"
      append-to-body
      :close-on-click-modal="false"
    >
      <div class="image-task-large-preview" @click.stop.prevent>
        <img
          v-if="largePreviewUrl"
          :src="largePreviewUrl"
          alt="生成结果大图"
          class="image-task-large-img"
          @click.stop.prevent
          @dragstart.prevent
        />
      </div>
      <template v-if="(previewRow?.result_urls_parsed?.length || 0) > 1" #footer>
        <el-button @click="switchLargePreview(-1)">上一张</el-button>
        <span style="margin:0 12px;color:var(--el-text-color-secondary)">
          {{ largePreviewIndex + 1 }} / {{ previewRow?.result_urls_parsed?.length || 0 }}
        </span>
        <el-button @click="switchLargePreview(1)">下一张</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.image-task-thumb-button {
  width: 200px;
  height: 200px;
  padding: 0;
  border: 0;
  border-radius: 4px;
  background: transparent;
  cursor: zoom-in;
  overflow: hidden;
}

.error-reason {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.error-detail {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-regular);
}

.image-task-thumb {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  border-radius: 4px;
}

.image-task-large-preview {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 320px;
  max-height: 72vh;
  overflow: auto;
}

.image-task-large-img {
  display: block;
  max-width: 100%;
  max-height: 72vh;
  object-fit: contain;
  user-select: none;
}
</style>
