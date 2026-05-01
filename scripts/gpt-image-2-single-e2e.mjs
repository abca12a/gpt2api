#!/usr/bin/env node
/**
 * gpt-image-2 1k/2k/4k 真单联调工具。
 *
 * 能力:
 * - 用 API Key 发起 1k/2k/4k 单张异步请求并轮询终态。
 * - 复用已有号池 task_id / 下游 task_id 做核对。
 * - 汇总 resolution、provider_trace、任务状态、号池关键日志、下游 billing_context、前端 pricing 展示。
 */

import { argv, env, exit } from 'node:process'
import { writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)

const args = parseArgs(argv.slice(2))

const BASE = stripSlash(args.base || env.GPT2API_BASE || 'http://127.0.0.1:8080')
const API_KEY = args['api-key'] || env.GPT2API_IMAGE_TEST_KEY || env.GPT2API_API_KEY || ''
const RESOLUTIONS = csv(args.resolutions || '1k,2k,4k').map(normalizeResolution).filter(Boolean)
const PROMPT = args.prompt || 'gpt-image-2 resolution routing smoke: draw a small clean verification card'
const SIZE = args.size || '1:1'
const N = clampInt(args.n, 1, 1, 4)
const POLL_INTERVAL_MS = clampInt(args['poll-interval-sec'], 5, 1, 60) * 1000
const TIMEOUT_MS = clampInt(args['timeout-sec'], 720, 30, 1800) * 1000
const FRONTEND_PRICING_URLS = csv(args['frontend-pricing-urls'] || env.GPT_IMAGE2_PRICING_URLS || 'https://dimilinks.com/api/pricing,https://preview.dimilinks.com/api/pricing')
const TASK_IDS = csv(args['task-ids'] || '')
const DOWNSTREAM_TASK_IDS = csv(args['downstream-task-ids'] || '')
const JSON_OUT = args['json-out'] || ''
const SKIP_LOCAL_DB = boolArg(args['skip-local-db'])
const SKIP_LOGS = boolArg(args['skip-logs'])
const SKIP_DOWNSTREAM_DB = boolArg(args['skip-downstream-db'])
const DOWNSTREAM_SSH = args['downstream-ssh'] || env.NEW_API_SSH || 'root@212.50.232.214'
const DOWNSTREAM_SSH_OPTS = args['downstream-ssh-opts'] || env.NEW_API_SSH_OPTS || ''
const DOWNSTREAM_DB_CONTAINER = args['downstream-db-container'] || env.NEW_API_DB_CONTAINER || 'new-api-postgres-local'
const DOWNSTREAM_DB_NAME = args['downstream-db-name'] || env.NEW_API_DB_NAME || 'new-api'
const DOWNSTREAM_DB_USER = args['downstream-db-user'] || env.NEW_API_DB_USER || 'root'
const LOCAL_MYSQL_CONTAINER = args['mysql-container'] || env.GPT2API_MYSQL_CONTAINER || 'gpt2api-mysql'
const SERVER_CONTAINER = args['server-container'] || env.GPT2API_SERVER_CONTAINER || 'gpt2api-server'

const defaultUnitPrices = { '1k': 0, '2k': 0.06, '4k': 0.12 }
const terminalStatuses = new Set(['success', 'failed', 'completed', 'cancelled', 'canceled'])

main().catch((err) => {
  console.error(`\n[FATAL] ${err.stack || err.message || err}`)
  exit(2)
})

async function main() {
  const startedAt = new Date()
  const report = {
    tool: 'gpt-image-2-single-e2e',
    started_at: startedAt.toISOString(),
    base: BASE,
    request: { model: 'gpt-image-2', resolutions: RESOLUTIONS, n: N, size: SIZE },
    frontend_pricing: [],
    downstream_tasks: [],
    cases: [],
    summary: { pass: 0, warn: 0, fail: 0 },
  }

  printHeader('gpt-image-2 单联调')
  console.log(`base: ${BASE}`)
  console.log(`resolutions: ${RESOLUTIONS.join(', ')}`)
  console.log(`mode: ${API_KEY ? 'submit/poll' : 'inspect only'}${TASK_IDS.length ? ' + existing task ids' : ''}`)

  report.frontend_pricing = await fetchFrontendPricing(FRONTEND_PRICING_URLS)
  const pricing = choosePricingMap(report.frontend_pricing)

  if (DOWNSTREAM_TASK_IDS.length && !SKIP_DOWNSTREAM_DB) {
    report.downstream_tasks = await queryDownstreamTasks(DOWNSTREAM_TASK_IDS)
    for (const row of report.downstream_tasks) {
      const upstreamTaskID = pickUpstreamTaskID(row)
      if (upstreamTaskID && !TASK_IDS.includes(upstreamTaskID)) {
        TASK_IDS.push(upstreamTaskID)
      }
    }
  }

  if (API_KEY) {
    for (const resolution of RESOLUTIONS) {
      const result = await submitAndPollCase(resolution)
      report.cases.push(result)
    }
  }

  if (TASK_IDS.length) {
    const existing = await inspectExistingTasks(TASK_IDS)
    for (const task of existing) {
      const resolution = normalizeResolution(task.resolution || task.provider_trace?.resolution || '')
      report.cases.push(buildCaseFromTask({
        label: `existing:${task.task_id}`,
        requested_resolution: resolution || 'unknown',
        submit: null,
        task,
      }))
    }
  }

  if (!report.cases.length && report.downstream_tasks.length) {
    for (const row of report.downstream_tasks) {
      report.cases.push(buildCaseFromTask({
        label: `downstream:${row.task_id}`,
        requested_resolution: normalizeResolution(row.billing_context?.resolution || row.private_data?.resolution || ''),
        submit: null,
        task: {
          task_id: row.upstream_task_id || row.task_id,
          status: row.status || 'unknown',
          resolution: row.billing_context?.resolution || row.private_data?.resolution || '',
          error: row.query_error || row.fail_reason || '',
          inspect_error: row.query_error || '',
        },
      }))
    }
  }

  if (!SKIP_LOCAL_DB) {
    await enrichCasesFromLocalDB(report.cases)
  }
  if (!SKIP_LOGS) {
    await enrichCasesWithLogs(report.cases)
  }

  for (const c of report.cases) {
    c.checks = buildChecks(c, report.downstream_tasks, pricing)
    for (const check of c.checks) {
      report.summary[check.level]++
    }
  }
  report.finished_at = new Date().toISOString()

  printReport(report)
  if (JSON_OUT) {
    await writeFile(JSON_OUT, JSON.stringify(report, null, 2) + '\n')
    console.log(`\nJSON 已写入: ${JSON_OUT}`)
  }
  exit(report.summary.fail > 0 ? 1 : 0)
}

async function submitAndPollCase(resolution) {
  const body = {
    model: 'gpt-image-2',
    prompt: PROMPT,
    n: N,
    size: SIZE,
    resolution,
  }
  const path = '/v1/images/generations?async=true'
  const submit = await httpJSON('POST', BASE + path, {
    headers: { Authorization: `Bearer ${API_KEY}` },
    body,
  })
  const taskID = extractTaskID(submit.body)
  const label = `${resolution}:${taskID || 'submit-failed'}`
  if (!taskID) {
    return buildCaseFromTask({
      label,
      requested_resolution: resolution,
      submit,
      task: null,
      submit_error: `提交响应中没有 task_id: ${shortJSON(submit.body)}`,
    })
  }
  const task = await pollTask(taskID)
  return buildCaseFromTask({ label, requested_resolution: resolution, submit, task })
}

async function pollTask(taskID) {
  const deadline = Date.now() + TIMEOUT_MS
  let last = null
  while (Date.now() < deadline) {
    const r = await httpJSON('GET', `${BASE}/v1/images/tasks/${encodeURIComponent(taskID)}`, {
      headers: { Authorization: `Bearer ${API_KEY}` },
    })
    last = unwrapEnvelope(r.body)
    const status = normalizeStatus(last?.status)
    if (terminalStatuses.has(status)) {
      return last
    }
    await sleep(POLL_INTERVAL_MS)
  }
  return {
    ...(last || {}),
    task_id: taskID,
    status: last?.status || 'timeout',
    poll_timeout: true,
  }
}

async function inspectExistingTasks(taskIDs) {
  if (API_KEY) {
    const out = []
    for (const taskID of taskIDs) {
      const r = await httpJSON('GET', `${BASE}/v1/images/tasks/${encodeURIComponent(taskID)}`, {
        headers: { Authorization: `Bearer ${API_KEY}` },
      })
      out.push(unwrapEnvelope(r.body))
    }
    return out
  }
  if (SKIP_LOCAL_DB) {
    return taskIDs.map((taskID) => ({ task_id: taskID, status: 'unknown', note: '未提供 API key 且跳过本地 DB' }))
  }
  try {
    return await queryLocalImageTasks(taskIDs)
  } catch (err) {
    return taskIDs.map((taskID) => ({
      task_id: taskID,
      status: 'unknown',
      inspect_error: err.message,
    }))
  }
}

function buildCaseFromTask({ label, requested_resolution, submit, task, submit_error }) {
  const trace = task?.provider_trace || null
  const final = trace?.final || {}
  const fallback = trace?.fallback || {}
  return {
    label,
    requested_resolution,
    submitted_task_id: extractTaskID(submit?.body) || task?.task_id || task?.id || '',
    task_id: task?.task_id || task?.id || '',
    status: normalizeStatus(task?.status || ''),
    resolution: normalizeResolution(task?.resolution || trace?.resolution || ''),
    image_count: Array.isArray(task?.data) ? task.data.length : numberOrNull(task?.result_url_count),
    provider: final.provider || '',
    provider_channel: final.channel_name || '',
    provider_account_id: final.account_id || 0,
    provider_account_plan: final.account_plan_type || '',
    fallback_triggered: Boolean(fallback?.triggered),
    provider_trace_summary: task?.provider_trace_summary || summarizeTrace(trace),
    provider_trace: trace,
    timing: task?.timing || trace?.timing || null,
    error_code: task?.error_code || task?.error?.code || '',
    error_message: task?.error_message || task?.error?.message || task?.error || submit_error || task?.inspect_error || '',
    submit,
    task,
  }
}

async function fetchFrontendPricing(urls) {
  const out = []
  for (const url of urls) {
    try {
      const r = await httpJSON('GET', url, {}, 15000)
      const data = unwrapEnvelope(r.body)
      const model = findGPTImage2Pricing(data)
      out.push({
        url,
        ok: r.status >= 200 && r.status < 300 && Boolean(model),
        status: r.status,
        model_price: model?.model_price ?? model?.price ?? null,
        resolution_options: model?.resolution_options || null,
        pricing_version: model?.pricing_version || null,
      })
    } catch (err) {
      out.push({ url, ok: false, error: err.message })
    }
  }
  return out
}

function findGPTImage2Pricing(data) {
  const list = Array.isArray(data) ? data
    : Array.isArray(data?.models) ? data.models
      : Array.isArray(data?.items) ? data.items
        : Array.isArray(data?.data) ? data.data
          : []
  return list.find((m) => m?.model_name === 'gpt-image-2' || m?.name === 'gpt-image-2' || m?.slug === 'gpt-image-2') || null
}

function choosePricingMap(frontendPricing) {
  const hit = frontendPricing.find((p) => p.ok && p.resolution_options)
  const map = { ...defaultUnitPrices }
  const options = Array.isArray(hit?.resolution_options) ? hit.resolution_options : []
  for (const opt of options) {
    const resolution = normalizeResolution(opt.resolution || opt.value || opt.key || opt.name)
    const price = Number(opt.model_price ?? opt.price ?? opt.quota ?? opt.value_price)
    if (resolution && Number.isFinite(price)) {
      map[resolution] = price
    }
  }
  return { map, source: hit?.url || '', ok: Boolean(hit) }
}

async function queryLocalImageTasks(taskIDs) {
  const ids = cleanIDs(taskIDs)
  if (!ids.length) return []
  const sql = `
SELECT id, task_id, status, n, size, upscale, account_id, estimated_credit, credit_cost, error,
       COALESCE(provider_trace, ''), COALESCE(JSON_LENGTH(file_ids), 0), COALESCE(JSON_LENGTH(result_urls), 0),
       UNIX_TIMESTAMP(created_at), UNIX_TIMESTAMP(started_at), UNIX_TIMESTAMP(finished_at)
  FROM image_tasks
 WHERE task_id IN (${ids.map((id) => `'${id}'`).join(',')});`
  const script = `db="$${'{'}MYSQL_DATABASE:-gpt2api${'}'}"; pass="$${'{'}MYSQL_ROOT_PASSWORD:-root${'}'}"; MYSQL_PWD="$pass" mysql -uroot "$db" --batch --raw --skip-column-names -e ${shellQuote(sql)}`
  const { stdout } = await execFileAsync('docker', ['exec', LOCAL_MYSQL_CONTAINER, 'sh', '-lc', script], { maxBuffer: 10 * 1024 * 1024 })
  return parseLocalTaskRows(stdout)
}

async function enrichCasesFromLocalDB(cases) {
  const ids = cleanIDs(cases.map((c) => c.task_id || c.submitted_task_id).filter(Boolean))
  if (!ids.length) return
  try {
    const rows = await queryLocalImageTasks(ids)
    const byID = new Map(rows.map((r) => [r.task_id, r]))
    for (const c of cases) {
      const row = byID.get(c.task_id || c.submitted_task_id)
      if (!row) continue
      c.local_db = row
      if (!c.provider_trace && row.provider_trace) {
        c.provider_trace = row.provider_trace
        c.provider_trace_summary = c.provider_trace_summary || summarizeTrace(row.provider_trace)
      }
      c.resolution = c.resolution || normalizeResolution(row.provider_trace?.resolution || row.upscale || '')
      c.status = c.status || normalizeStatus(row.status)
    }
  } catch (err) {
    for (const c of cases) {
      c.local_db_error = err.message
    }
  }
}

function parseLocalTaskRows(stdout) {
  return stdout.trim().split('\n').filter(Boolean).map((line) => {
    const cols = line.split('\t')
    const trace = parseJSON(cols[10])
    return {
      id: Number(cols[0]),
      task_id: cols[1],
      status: cols[2],
      n: Number(cols[3]),
      size: cols[4],
      upscale: cols[5],
      account_id: Number(cols[6]),
      estimated_credit: Number(cols[7]),
      credit_cost: Number(cols[8]),
      error: cols[9],
      provider_trace: trace,
      file_id_count: Number(cols[11]),
      result_url_count: Number(cols[12]),
      created_at: unixToISO(cols[13]),
      started_at: unixToISO(cols[14]),
      finished_at: unixToISO(cols[15]),
    }
  })
}

async function enrichCasesWithLogs(cases) {
  const ids = cleanIDs(cases.map((c) => c.task_id || c.submitted_task_id).filter(Boolean))
  if (!ids.length) return
  for (const c of cases) {
    const id = cleanIDs([c.task_id || c.submitted_task_id])[0]
    if (!id) continue
    try {
      const cmd = `docker logs --since 4h ${shellQuote(SERVER_CONTAINER)} 2>&1 | grep -F ${shellQuote(id)} | tail -n 20`
      const { stdout } = await execFileAsync('bash', ['-lc', cmd], { maxBuffer: 2 * 1024 * 1024 })
      c.key_logs = stdout.trim().split('\n').filter(Boolean).map(trimLogLine)
    } catch (err) {
      c.key_logs = []
      c.key_logs_error = err.message
    }
  }
}

async function queryDownstreamTasks(taskIDs) {
  const ids = cleanIDs(taskIDs)
  if (!ids.length) return []
  const sql = `
SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
  SELECT id, task_id, status, quota, fail_reason, private_data, data, created_at, updated_at
    FROM tasks
   WHERE task_id IN (${ids.map((id) => `'${id}'`).join(',')})
   ORDER BY created_at DESC
) t;`
  const remote = [
    'docker', 'exec', DOWNSTREAM_DB_CONTAINER,
    'psql', '-U', DOWNSTREAM_DB_USER, '-d', DOWNSTREAM_DB_NAME,
    '-At', '-c', sql,
  ].map(shellQuote).join(' ')
  try {
    const { stdout } = await execFileAsync('ssh', [...downstreamSSHArgs(), DOWNSTREAM_SSH, remote], { maxBuffer: 10 * 1024 * 1024 })
    return normalizeDownstreamRows(parseJSON(stdout.trim()) || [])
  } catch (err) {
    return ids.map((task_id) => ({ task_id, query_error: err.message }))
  }
}

function normalizeDownstreamRows(rows) {
  return rows.map((row) => {
    const privateData = parseMaybeJSON(row.private_data)
    const data = parseMaybeJSON(row.data)
    return {
      ...row,
      private_data: privateData,
      data,
      billing_context: privateData?.billing_context || null,
      upstream_task_id: privateData?.upstream_task_id || privateData?.task_id || privateData?.upstream?.task_id || '',
      result_count: Array.isArray(data?.result?.data) ? data.result.data.length : null,
    }
  })
}

function pickUpstreamTaskID(row) {
  return cleanIDs([row?.upstream_task_id])[0] || ''
}

function buildChecks(c, downstreamRows, pricing) {
  const checks = []
  const pricingMap = pricing.map
  const requested = normalizeResolution(c.requested_resolution)
  const actualResolution = normalizeResolution(c.resolution || c.provider_trace?.resolution)
  const taskID = c.task_id || c.submitted_task_id
  const downstream = downstreamRows.find((row) => row.upstream_task_id === taskID || row.task_id === taskID)
  const billing = downstream?.billing_context || null
  const expectedUnit = pricingMap[requested] ?? defaultUnitPrices[requested]
  const expectedTotal = roundMoney(expectedUnit * (c.local_db?.n || N))

  addCheck(checks, actualResolution === requested, 'resolution', `请求 ${requested || '-'} / 号池回显 ${actualResolution || '-'}`)

  if (c.status) {
    addCheck(checks, terminalStatuses.has(c.status), 'task_status', `任务状态 ${c.status}`, c.status === 'success' ? 'pass' : 'warn')
  } else {
    checks.push({ level: 'warn', name: 'task_status', message: '没有拿到任务状态' })
  }

  const provider = c.provider || c.provider_trace?.final?.provider || ''
  if (requested === '1k') {
    const ok = provider === 'free_runner' || provider === 'account_runner' || /Free Runner|Account Runner/i.test(c.provider_trace_summary || '')
    addCheck(checks, ok, 'provider_route', `1k 实际 provider=${provider || '-'} trace=${c.provider_trace_summary || '-'}`)
  } else if (requested === '2k' || requested === '4k') {
    const externalOK = provider === 'codex' || provider === 'apimart'
    const fallbackOK = (provider === 'free_runner' || provider === 'account_runner') && c.fallback_triggered
    if (externalOK) {
      checks.push({ level: 'pass', name: 'provider_route', message: `${requested} 命中外置 provider=${provider} ${c.provider_channel || ''}`.trim() })
    } else if (fallbackOK) {
      checks.push({ level: 'warn', name: 'provider_route', message: `${requested} 外置失败后兜底到 ${provider}，按当前业务语义可接受，但需确认交付质量` })
    } else {
      checks.push({ level: 'fail', name: 'provider_route', message: `${requested} provider=${provider || '-'}，未看到 Codex/APIMart 或明确 fallback` })
    }
  }

  if (billing) {
    const billResolution = normalizeResolution(billing.resolution || billing.requested_resolution)
    addCheck(checks, billResolution === requested, 'billing_context.resolution', `billing_context.resolution=${billResolution || '-'}`)
    const modelPrice = Number(billing.model_price ?? billing.price ?? billing.unit_price)
    if (Number.isFinite(modelPrice)) {
      const ok = Math.abs(modelPrice - expectedUnit) < 0.000001
      addCheck(checks, ok, 'billing_context.model_price', `unit=${modelPrice}, expected=${expectedUnit}`)
    } else {
      checks.push({ level: 'warn', name: 'billing_context.model_price', message: 'billing_context 未看到 model_price/unit_price 字段' })
    }
    checks.push({ level: 'pass', name: 'billing_context.present', message: `下游任务 ${downstream.task_id} 已固化 billing_context，预期单张总价=${expectedTotal}` })
  } else if (DOWNSTREAM_TASK_IDS.length) {
    checks.push({ level: 'fail', name: 'billing_context.present', message: `下游任务未查到 billing_context: ${downstream?.query_error || 'private_data 缺失或无 upstream 映射'}` })
  } else {
    checks.push({ level: 'warn', name: 'billing_context.present', message: '未传 --downstream-task-ids，跳过后端 billing_context 核对' })
  }

  const pricingOK = pricing.ok
  checks.push({
    level: pricingOK ? 'pass' : 'warn',
    name: 'frontend_pricing',
    message: pricingOK
      ? `前端 pricing 可读(${pricing.source}): 1k=${pricingMap['1k']} 2k=${pricingMap['2k']} 4k=${pricingMap['4k']}`
      : `前端 pricing 未读到 resolution_options，脚本按默认值核对: 1k=${pricingMap['1k']} 2k=${pricingMap['2k']} 4k=${pricingMap['4k']}`,
  })

  if (c.local_db) {
    checks.push({
      level: 'pass',
      name: 'gpt2api_db',
      message: `号池 DB id=${c.local_db.id} account_id=${c.local_db.account_id} cost=${c.local_db.credit_cost} result_urls=${c.local_db.result_url_count}`,
    })
  } else if (!SKIP_LOCAL_DB) {
    checks.push({ level: 'warn', name: 'gpt2api_db', message: c.local_db_error || '未查到号池 DB 记录' })
  }

  return checks
}

function addCheck(checks, ok, name, message, okLevel = 'pass') {
  checks.push({ level: ok ? okLevel : 'fail', name, message })
}

function printReport(report) {
  printHeader('前端 pricing')
  for (const p of report.frontend_pricing) {
    const options = Array.isArray(p.resolution_options)
      ? p.resolution_options.map((o) => `${o.resolution || o.value}:${o.model_price ?? o.price}`).join(' ')
      : '无 resolution_options'
    console.log(`${p.ok ? 'PASS' : 'WARN'} ${p.url} ${p.status || ''} ${options}${p.error ? ` error=${p.error}` : ''}`)
  }

  if (report.downstream_tasks.length) {
    printHeader('下游任务 billing_context')
    for (const row of report.downstream_tasks) {
      console.log(`${row.task_id}: status=${row.status || '-'} quota=${row.quota ?? '-'} upstream=${row.upstream_task_id || '-'} billing=${shortJSON(row.billing_context || row.query_error || null)}`)
    }
  }

  printHeader('号池任务与核对结果')
  for (const c of report.cases) {
    console.log(`\n[${c.label}] status=${c.status || '-'} resolution=${c.resolution || '-'} provider=${c.provider || '-'} fallback=${c.fallback_triggered ? 'yes' : 'no'}`)
    console.log(`trace: ${c.provider_trace_summary || '-'}`)
    if (c.timing) console.log(`timing: ${shortJSON(c.timing)}`)
    if (c.error_message) console.log(`error: ${c.error_code || '-'} ${c.error_message}`)
    for (const check of c.checks || []) {
      console.log(`  ${check.level.toUpperCase()} ${check.name}: ${check.message}`)
    }
    if (Array.isArray(c.key_logs) && c.key_logs.length) {
      console.log('  logs:')
      for (const line of c.key_logs.slice(-6)) {
        console.log(`    ${line}`)
      }
    }
  }

  printHeader('汇总')
  console.log(`PASS=${report.summary.pass} WARN=${report.summary.warn} FAIL=${report.summary.fail}`)
}

async function httpJSON(method, url, { headers = {}, body } = {}, timeout = 30000) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeout)
  try {
    const h = { ...headers }
    let payload
    if (body !== undefined) {
      h['Content-Type'] = 'application/json'
      payload = JSON.stringify(body)
    }
    const res = await fetch(url, { method, headers: h, body: payload, signal: controller.signal })
    const text = await res.text()
    let data = text
    try {
      data = text ? JSON.parse(text) : null
    } catch {}
    return { status: res.status, headers: Object.fromEntries(res.headers), body: data }
  } finally {
    clearTimeout(timer)
  }
}

function parseArgs(arr) {
  const out = {}
  for (let i = 0; i < arr.length; i++) {
    const a = arr[i]
    if (!a.startsWith('--')) continue
    const key = a.slice(2)
    const value = arr[i + 1] && !arr[i + 1].startsWith('--') ? arr[++i] : 'true'
    out[key] = value
  }
  return out
}

function stripSlash(value) { return String(value || '').replace(/\/+$/, '') }
function csv(value) { return String(value || '').split(',').map((s) => s.trim()).filter(Boolean) }
function boolArg(value) { return value === true || value === 'true' || value === '1' }
function clampInt(value, fallback, min, max) {
  const n = Number.parseInt(value ?? fallback, 10)
  if (!Number.isFinite(n)) return fallback
  return Math.max(min, Math.min(max, n))
}
function normalizeResolution(value) {
  const v = String(value || '').trim().toLowerCase()
  if (['1k', '1024', '1024x1024', 'standard'].includes(v)) return '1k'
  if (['2k', '2048', 'hd'].includes(v)) return '2k'
  if (['4k', '4096', 'uhd'].includes(v)) return '4k'
  return ''
}
function normalizeStatus(value) {
  const v = String(value || '').trim().toLowerCase()
  if (v === 'completed') return 'success'
  return v
}
function extractTaskID(body) {
  const data = unwrapEnvelope(body)
  return data?.task_id || data?.id || data?.data?.[0]?.task_id || body?.data?.[0]?.task_id || ''
}
function unwrapEnvelope(body) {
  if (body && typeof body === 'object' && 'code' in body && 'data' in body) return body.data
  return body
}
function parseJSON(value) {
  if (!value) return null
  try { return JSON.parse(value) } catch { return null }
}
function parseMaybeJSON(value) {
  if (value && typeof value === 'object') return value
  return parseJSON(value)
}
function cleanIDs(ids) {
  return ids.map((id) => String(id || '').trim()).filter((id) => /^[A-Za-z0-9_-]+$/.test(id))
}
function shellQuote(s) {
  return `'${String(s).replace(/'/g, `'\\''`)}'`
}
function shortJSON(value) {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return text && text.length > 500 ? `${text.slice(0, 500)}...` : text
}
function summarizeTrace(trace) {
  if (!trace) return ''
  const steps = Array.isArray(trace.steps) ? trace.steps : []
  if (!steps.length) return trace.final?.provider || trace.original?.provider || ''
  return steps.map((s) => {
    const name = s.channel_name ? `${s.provider}(${s.channel_name})` : s.provider
    return s.reason_code ? `${name}[${s.reason_code}]` : name
  }).filter(Boolean).join(' -> ')
}
function numberOrNull(value) {
  const n = Number(value)
  return Number.isFinite(n) ? n : null
}
function unixToISO(value) {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? new Date(n * 1000).toISOString() : null
}
function roundMoney(value) {
  return Math.round(Number(value || 0) * 1_000_000) / 1_000_000
}
function trimLogLine(line) {
  return line.replace(/\s+/g, ' ').slice(0, 600)
}
function downstreamSSHArgs() {
  if (DOWNSTREAM_SSH_OPTS) {
    return DOWNSTREAM_SSH_OPTS.match(/(?:[^\s"]+|"[^"]*")+/g)?.map((s) => s.replace(/^"|"$/g, '')) || []
  }
  const identityFile = `${env.HOME || '/home/ubuntu'}/.ssh/cliproxyapi_212_50_232_214_ed25519`
  if (existsSync(identityFile)) {
    return ['-i', identityFile, '-o', 'IdentitiesOnly=yes']
  }
  return []
}
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)) }
function printHeader(title) { console.log(`\n=== ${title} ===`) }
