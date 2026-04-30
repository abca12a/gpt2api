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
  result_count: number
  has_result: boolean
  error: string
  error_code?: string
  error_message?: string
  error_detail?: string
  error_layer?: string
  error_layer_label?: string
  provider_trace?: ProviderTrace | null
  provider_trace_summary?: string
  credit_cost: number
  estimated_credit: number
  created_at: string
  started_at?: string | null
  finished_at?: string | null
  detail_loaded?: boolean
}

interface ProviderTraceEndpoint {
  provider?: string
  channel_id?: number
  channel_name?: string
  account_id?: number
  account_plan_type?: string
  status?: string
}

interface ProviderTraceFallback {
  triggered?: boolean
  reason_code?: string
  reason_detail?: string
  from_provider?: string
  from_channel_id?: number
  from_channel_name?: string
}

interface ProviderTraceStep extends ProviderTraceEndpoint {
  order?: number
  upstream_request_id?: string
  downstream_status?: string
  error_layer?: string
  error_layer_label?: string
  reason_code?: string
  reason_detail?: string
}

interface ProviderTrace {
  request_id?: string
  task_id?: string
  upstream_request_id?: string
  downstream_status?: string
  error_layer?: string
  error_layer_label?: string
  original?: ProviderTraceEndpoint
  fallback?: ProviderTraceFallback | null
  final?: ProviderTraceEndpoint
  steps?: ProviderTraceStep[]
}

interface ProviderHitStat {
  provider: string
  display_name: string
  attempted: number
  skipped: number
  first_selected: number
  final_selected: number
  success: number
  failed: number
  fallback_from: number
}

interface ProviderTransitionStat {
  from_provider: string
  to_provider: string
  display: string
  count: number
}

interface ProviderTraceStats {
  window_hours: number
  total: number
  success: number
  failed: number
  fallback_triggered: number
  providers: ProviderHitStat[]
  transitions: ProviderTransitionStat[]
  accounts: AccountRealtimeStat[]
}

interface AccountRealtimeStat {
  account_id: number
  email?: string
  plan_type?: string
  status?: string
  recent_total: number
  success: number
  failed: number
  success_rate: number
  avg_first_packet_ms?: number
  avg_completion_ms?: number
  consecutive_failures: number
  last_error_type?: string
  last_task_id?: string
  last_status?: string
  last_finished_at?: number
  cooldown_until?: number
  cooldown_remaining_ms?: number
  in_cooldown: boolean
  circuit_open: boolean
}

const loading = ref(false)
const rows = ref<TaskRow[]>([])
const total = ref(0)
const statsLoading = ref(false)
const statsHours = ref(24)
const stats = ref<ProviderTraceStats | null>(null)
const filter = reactive({
  keyword: '',
  status: '',
  page: 1,
  page_size: 20,
})

async function fetchStats() {
  statsLoading.value = true
  try {
    stats.value = await http.get<any, any>('/api/admin/image-tasks/stats', {
      params: { hours: statsHours.value },
    })
  } finally {
    statsLoading.value = false
  }
}

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
const previewLoading = ref(false)
async function ensureTaskDetails(row: TaskRow) {
  if (row.detail_loaded) return
  previewLoading.value = true
  try {
    const data = await http.get<any, any>(`/api/admin/image-tasks/${encodeURIComponent(row.task_id)}/images`)
    row.result_urls_parsed = data.result_urls_parsed || []
    row.result_count = data.result_count ?? row.result_urls_parsed.length
    row.has_result = row.result_urls_parsed.length > 0 || row.has_result || row.status === 'success'
    row.error = data.error ?? row.error
    row.error_code = data.error_code ?? row.error_code
    row.error_message = data.error_message ?? row.error_message
    row.error_detail = data.error_detail ?? row.error_detail
    row.provider_trace = data.provider_trace ?? row.provider_trace
    row.provider_trace_summary = data.provider_trace_summary ?? row.provider_trace_summary
    row.detail_loaded = true
  } finally {
    previewLoading.value = false
  }
}
async function openPreview(row: TaskRow) {
  previewRow.value = row
  previewDlg.value = true
  await ensureTaskDetails(row)
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

const errorLayerLabel: Record<string, string> = {
  gateway_entry: '号池入口',
  task_queue: '任务队列',
  polling: '轮询',
  gateway_fallback: '号池兜底',
  downstream_backend: '下游后端',
  downstream_apimart: '下游 apimart',
}

const errorTagType: Record<string, 'danger' | 'warning' | 'info'> = {
  content_moderation: 'danger',
  invalid_request_error: 'warning',
  upstream_error: 'warning',
  poll_timeout: 'warning',
  interrupted: 'info',
}

const providerLabel: Record<string, string> = {
  codex: 'Codex',
  apimart: 'APIMart',
  openai: 'OpenAI',
  gemini: 'Gemini',
  account_runner: '内置账号池',
  free_runner: 'Free Runner',
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
  return errorCodeLabel[code] || code || (row.status === 'failed' ? '失败详情' : '-')
}

function traceErrorLayer(row: TaskRow) {
  const layer = row.error_layer || row.provider_trace?.error_layer || ''
  const label = row.error_layer_label || row.provider_trace?.error_layer_label || errorLayerLabel[layer]
  return label || layer || ''
}

function traceRequestID(row: TaskRow) {
  return row.provider_trace?.request_id || ''
}

function traceUpstreamID(row: TaskRow) {
  return row.provider_trace?.upstream_request_id || ''
}

function traceDownstreamStatus(row: TaskRow) {
  return row.provider_trace?.downstream_status || ''
}

function errorDetail(row: TaskRow) {
  const parsed = splitTaskError(row.error)
  return row.error_message || row.error_detail || parsed.detail || row.error || (row.status === 'failed' ? '点击查看加载失败详情' : '')
}

function errorType(row: TaskRow) {
  const code = taskErrorCode(row)
  return errorTagType[code] || 'warning'
}

function errorCopyText(row: TaskRow) {
  return [
    traceErrorLayer(row) ? `错误层级:${traceErrorLayer(row)}` : '',
    traceRequestID(row) ? `request_id:${traceRequestID(row)}` : '',
    row.task_id ? `task_id:${row.task_id}` : '',
    traceUpstreamID(row) ? `upstream_request_id:${traceUpstreamID(row)}` : '',
    traceDownstreamStatus(row) ? `downstream_status:${traceDownstreamStatus(row)}` : '',
    row.error_message,
    row.error_detail || row.error,
  ]
    .filter((v, idx, arr) => v && arr.indexOf(v) === idx)
    .join('\n')
}

function resultCount(row: TaskRow) {
  return row.result_count || row.result_urls_parsed?.length || (row.status === 'success' ? row.n : 0)
}

function resultActionText(row: TaskRow) {
  if (row.status === 'failed' || row.error || row.error_message) return '查看失败'
  const count = resultCount(row)
  if (row.status === 'success' || row.has_result || count > 0) return `查看结果${count ? `(${count})` : ''}`
  if (row.status === 'running') return '查看进度'
  if (row.status === 'queued' || row.status === 'dispatched') return '查看状态'
  return '查看详情'
}

function providerName(provider = '') {
  return providerLabel[provider] || provider || '未知渠道'
}

function providerStat(provider = '') {
  return stats.value?.providers?.find((item) => item.provider === provider)
}

function providerFirst(provider = '') {
  return providerStat(provider)?.first_selected || 0
}

function providerFinal(provider = '') {
  return providerStat(provider)?.final_selected || 0
}

function providerFallbackOut(provider = '') {
  return providerStat(provider)?.fallback_from || 0
}

function statsSuccessRate() {
  if (!stats.value?.total) return '0%'
  return `${((stats.value.success / stats.value.total) * 100).toFixed(1)}%`
}

function formatMs(ms?: number) {
  if (!ms) return '-'
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  return `${ms}ms`
}

function accountStatusType(row: AccountRealtimeStat): 'success' | 'danger' | 'warning' | 'info' {
  if (row.circuit_open || row.in_cooldown) return 'danger'
  if (row.consecutive_failures > 0 || row.failed > 0) return 'warning'
  return 'success'
}

function cooldownLabel(row: AccountRealtimeStat) {
  if (!row.in_cooldown && !row.circuit_open) return '正常'
  const ms = row.cooldown_remaining_ms || 0
  return ms > 0 ? `冷却中 ${formatMs(ms)}` : '熔断/冷却'
}

function traceActorLabel(endpoint?: ProviderTraceEndpoint | null) {
  if (!endpoint) return '-'
  const base = providerName(endpoint.provider || '')
  if (endpoint.provider === 'free_runner' || endpoint.provider === 'account_runner') {
    if (endpoint.account_id && endpoint.account_plan_type) return `${base} #${endpoint.account_id}/${endpoint.account_plan_type}`
    if (endpoint.account_id) return `${base} #${endpoint.account_id}`
    return base
  }
  if (endpoint.channel_name) return `${base} / ${endpoint.channel_name}`
  return base
}

function traceStatusType(status = ''): 'success' | 'danger' | 'warning' | 'info' {
  switch (status) {
    case 'success':
      return 'success'
    case 'failed':
      return 'danger'
    case 'running':
      return 'warning'
    default:
      return 'info'
  }
}

function traceFallbackReason(row: TaskRow) {
  const fallback = row.provider_trace?.fallback
  if (!fallback?.triggered) return ''
  return [fallback.reason_code, fallback.reason_detail].filter(Boolean).join(': ')
}

function traceSteps(row: TaskRow) {
  return row.provider_trace?.steps || []
}

function providerTraceSummary(row: TaskRow) {
  return row.provider_trace_summary || (row.provider_trace?.final ? `${traceActorLabel(row.provider_trace.original)} -> ${traceActorLabel(row.provider_trace.final)}` : '')
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

onMounted(() => {
  fetchStats()
  fetchList()
})
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <h2 class="page-title" style="margin:0">生成记录</h2>
      <div style="color:var(--el-text-color-secondary);font-size:13px;margin:4px 0 14px">
        全站图片生成任务历史,含后端顾客、号池用户、提示词、生成结果与耗时。
      </div>

      <div class="trace-stats-panel" v-loading="statsLoading">
        <div class="trace-stats-head">
          <div class="trace-stats-title">渠道命中统计</div>
          <div class="flex-wrap-gap">
            <el-select v-model="statsHours" style="width:140px" @change="fetchStats">
              <el-option label="最近 6 小时" :value="6" />
              <el-option label="最近 24 小时" :value="24" />
              <el-option label="最近 72 小时" :value="72" />
              <el-option label="最近 7 天" :value="168" />
            </el-select>
            <el-button link type="primary" @click="fetchStats">刷新统计</el-button>
          </div>
        </div>
        <div v-if="stats" class="trace-stats-grid">
          <div class="trace-stat-card">
            <div class="trace-stat-label">总任务</div>
            <div class="trace-stat-value">{{ stats.total }}</div>
            <div class="trace-stat-sub">最近 {{ stats.window_hours }} 小时</div>
          </div>
          <div class="trace-stat-card">
            <div class="trace-stat-label">成功率</div>
            <div class="trace-stat-value">{{ statsSuccessRate() }}</div>
            <div class="trace-stat-sub">成功 {{ stats.success }} / 失败 {{ stats.failed }}</div>
          </div>
          <div class="trace-stat-card">
            <div class="trace-stat-label">触发兜底</div>
            <div class="trace-stat-value">{{ stats.fallback_triggered }}</div>
            <div class="trace-stat-sub">有 fallback trace 的任务数</div>
          </div>
          <div class="trace-stat-card">
            <div class="trace-stat-label">Codex 命中</div>
            <div class="trace-stat-value">{{ providerFirst('codex') }} / {{ providerFinal('codex') }}</div>
            <div class="trace-stat-sub">首跳 / 最终命中</div>
          </div>
          <div class="trace-stat-card">
            <div class="trace-stat-label">APIMart 最终命中</div>
            <div class="trace-stat-value">{{ providerFinal('apimart') }}</div>
            <div class="trace-stat-sub">Codex 转出 {{ providerFallbackOut('codex') }}</div>
          </div>
          <div class="trace-stat-card">
            <div class="trace-stat-label">Free Runner 最终命中</div>
            <div class="trace-stat-value">{{ providerFinal('free_runner') }}</div>
            <div class="trace-stat-sub">用于观察兜底落地频率</div>
          </div>
        </div>
        <div v-if="stats?.transitions?.length" class="trace-transition-row">
          <el-tag
            v-for="item in stats.transitions"
            :key="`${item.from_provider}-${item.to_provider}`"
            size="small"
            effect="plain"
          >
            {{ item.display }} · {{ item.count }}
          </el-tag>
        </div>
        <div v-if="stats?.accounts?.length" class="account-realtime-panel">
          <div class="trace-stats-title">账号级实时统计（最近 {{ stats.window_hours }} 小时）</div>
          <el-table :data="stats.accounts" size="small" border style="margin-top:8px">
            <el-table-column label="账号" min-width="210">
              <template #default="{ row }">
                <div>#{{ row.account_id }} · {{ row.email || '-' }}</div>
                <div class="trace-subtext">{{ row.plan_type || '-' }} / {{ row.status || '-' }}</div>
              </template>
            </el-table-column>
            <el-table-column label="成功率" width="115">
              <template #default="{ row }">
                <div>{{ row.success_rate?.toFixed ? row.success_rate.toFixed(2) : row.success_rate }}%</div>
                <div class="trace-subtext">{{ row.success }} / {{ row.recent_total }}</div>
              </template>
            </el-table-column>
            <el-table-column label="首包/完成" width="145">
              <template #default="{ row }">
                <div>{{ formatMs(row.avg_first_packet_ms) }}</div>
                <div class="trace-subtext">完成 {{ formatMs(row.avg_completion_ms) }}</div>
              </template>
            </el-table-column>
            <el-table-column label="连续失败" width="110">
              <template #default="{ row }">
                <el-tag :type="row.consecutive_failures ? 'danger' : 'success'" size="small">
                  {{ row.consecutive_failures }}
                </el-tag>
                <div class="trace-subtext">{{ row.last_error_type || '-' }}</div>
              </template>
            </el-table-column>
            <el-table-column label="冷却/熔断" width="135">
              <template #default="{ row }">
                <el-tag :type="accountStatusType(row)" size="small">{{ cooldownLabel(row) }}</el-tag>
              </template>
            </el-table-column>
          </el-table>
        </div>
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
        <el-table-column label="渠道链路" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <div v-if="providerTraceSummary(row)" class="trace-summary">
              <div>{{ providerTraceSummary(row) }}</div>
              <div v-if="traceErrorLayer(row)" class="trace-subtext trace-layer">
                错误层级: {{ traceErrorLayer(row) }}
              </div>
              <div v-if="traceFallbackReason(row)" class="trace-subtext">
                兜底原因: {{ traceFallbackReason(row) }}
              </div>
            </div>
            <span v-else style="color:var(--el-text-color-secondary)">-</span>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="80">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click="openPreview(row)">
              {{ resultActionText(row) }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column label="失败原因" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <div v-if="row.error || row.error_message || row.status === 'failed'" class="error-reason">
              <el-tag :type="errorType(row)" size="small">{{ errorReason(row) }}</el-tag>
              <el-tag v-if="traceErrorLayer(row)" type="info" size="small">{{ traceErrorLayer(row) }}</el-tag>
              <button type="button" class="error-detail-btn" :title="row.error_detail || row.error" @click="openPreview(row)">
                {{ errorDetail(row) }}
              </button>
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
    <el-dialog v-model="previewDlg" title="生成任务详情" width="680px">
      <div v-if="previewRow" v-loading="previewLoading">
        <div style="font-size:13px;color:var(--el-text-color-secondary);margin-bottom:10px;word-break:break-all">
          {{ previewRow.prompt }}
        </div>
        <div v-if="previewRow.result_urls_parsed?.length" style="display:flex;flex-wrap:wrap;gap:8px">
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
        <div v-else-if="!previewLoading" class="empty-preview">
          {{ previewRow.status === 'failed' ? '本次任务没有生成图片' : '暂无可预览图片，任务可能还未完成' }}
        </div>
        <div v-if="previewRow.provider_trace" class="task-trace-panel">
          <div class="task-trace-title">渠道来源链路</div>
          <div class="task-trace-summary">{{ providerTraceSummary(previewRow) || '-' }}</div>
          <div class="task-trace-grid">
            <div class="task-trace-label">原始命中</div>
            <div>{{ traceActorLabel(previewRow.provider_trace.original) }}</div>
            <div class="task-trace-label">最终渠道</div>
            <div>{{ traceActorLabel(previewRow.provider_trace.final) }}</div>
            <div class="task-trace-label">request_id</div>
            <div class="trace-id-text">{{ traceRequestID(previewRow) || '-' }}</div>
            <div class="task-trace-label">task_id</div>
            <div class="trace-id-text">{{ previewRow.task_id || previewRow.provider_trace.task_id || '-' }}</div>
            <div class="task-trace-label">上游请求</div>
            <div class="trace-id-text">{{ traceUpstreamID(previewRow) || '-' }}</div>
            <div class="task-trace-label">转发状态</div>
            <div>{{ traceDownstreamStatus(previewRow) || '-' }}</div>
            <div class="task-trace-label">错误层级</div>
            <div>{{ traceErrorLayer(previewRow) || '-' }}</div>
            <div class="task-trace-label">兜底原因</div>
            <div>{{ traceFallbackReason(previewRow) || '-' }}</div>
          </div>
          <div v-if="traceSteps(previewRow).length" class="task-trace-steps">
            <div
              v-for="step in traceSteps(previewRow)"
              :key="`${step.order}-${step.provider}-${step.channel_name}-${step.account_id}`"
              class="task-trace-step"
            >
              <div class="task-trace-step-head">
                <span class="task-trace-step-order">#{{ step.order }}</span>
                <span>{{ traceActorLabel(step) }}</span>
                <el-tag :type="traceStatusType(step.status)" size="small">{{ step.status || 'unknown' }}</el-tag>
              </div>
              <div v-if="step.reason_code || step.reason_detail" class="task-trace-step-detail">
                {{ [step.reason_code, step.reason_detail].filter(Boolean).join(': ') }}
              </div>
              <div v-if="step.upstream_request_id || step.downstream_status || step.error_layer_label" class="task-trace-step-detail">
                {{ [
                  step.upstream_request_id ? `上游:${step.upstream_request_id}` : '',
                  step.downstream_status ? `状态:${step.downstream_status}` : '',
                  step.error_layer_label ? `层级:${step.error_layer_label}` : '',
                ].filter(Boolean).join(' · ') }}
              </div>
            </div>
          </div>
        </div>
        <div v-if="previewRow.status === 'failed' || previewRow.error || previewRow.error_message" class="task-error-panel">
          <div class="task-error-title">
            <el-tag :type="errorType(previewRow)" size="small">{{ errorReason(previewRow) }}</el-tag>
            <el-tag v-if="traceErrorLayer(previewRow)" type="info" size="small">{{ traceErrorLayer(previewRow) }}</el-tag>
            <el-button type="primary" link size="small" @click="copyError(previewRow)">复制失败原因</el-button>
          </div>
          <div class="task-error-message">{{ errorDetail(previewRow) || '暂无失败详情' }}</div>
          <div v-if="previewRow.error_detail && previewRow.error_detail !== previewRow.error_message" class="task-error-raw">
            原始详情:{{ previewRow.error_detail }}
          </div>
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

.trace-stats-panel {
  margin-bottom: 14px;
  padding: 14px;
  border-radius: 12px;
  background: linear-gradient(135deg, rgba(15, 23, 42, 0.02), rgba(13, 148, 136, 0.08));
  border: 1px solid rgba(15, 23, 42, 0.06);
}

.trace-stats-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.trace-stats-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.trace-stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 10px;
}

.trace-stat-card {
  padding: 12px;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.78);
  border: 1px solid rgba(15, 23, 42, 0.06);
}

.trace-stat-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.trace-stat-value {
  margin-top: 6px;
  font-size: 22px;
  font-weight: 700;
  color: var(--el-text-color-primary);
}

.trace-stat-sub {
  margin-top: 6px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.trace-transition-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}

.account-realtime-panel {
  margin-top: 14px;
}

.error-reason {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.error-detail,
.error-detail-btn {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-regular);
}

.error-detail-btn {
  padding: 0;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.error-detail-btn:hover {
  color: var(--el-color-primary);
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

.empty-preview {
  padding: 32px 0;
  text-align: center;
  color: var(--el-text-color-secondary);
}

.task-error-panel {
  margin-top: 14px;
  padding: 12px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
}

.trace-summary {
  min-width: 0;
}

.trace-subtext {
  margin-top: 2px;
  color: var(--el-text-color-secondary);
  font-size: 11px;
}

.trace-layer {
  color: var(--el-color-warning);
}

.trace-id-text {
  word-break: break-all;
  font-family: var(--el-font-family);
}

.task-trace-panel {
  margin-top: 14px;
  padding: 12px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  background: #f8fafc;
}

.task-trace-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.task-trace-summary {
  margin-top: 6px;
  color: var(--el-text-color-regular);
  word-break: break-all;
}

.task-trace-grid {
  margin-top: 10px;
  display: grid;
  grid-template-columns: 80px 1fr;
  gap: 6px 10px;
  font-size: 12px;
}

.task-trace-label {
  color: var(--el-text-color-secondary);
}

.task-trace-steps {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.task-trace-step {
  padding-top: 8px;
  border-top: 1px dashed var(--el-border-color);
}

.task-trace-step:first-child {
  padding-top: 0;
  border-top: 0;
}

.task-trace-step-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12px;
}

.task-trace-step-order {
  color: var(--el-text-color-secondary);
}

.task-trace-step-detail {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  word-break: break-all;
}

.task-error-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.task-error-message,
.task-error-raw {
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
}

.task-error-message {
  color: var(--el-color-danger);
}

.task-error-raw {
  margin-top: 6px;
  color: var(--el-text-color-secondary);
}
</style>
