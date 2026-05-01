import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildChecks,
  chooseOrderPricing,
  choosePricingMap,
  parsePriceMap,
} from './gpt-image-2-single-e2e.mjs'

test('parsePriceMap accepts reusable historical price maps', () => {
  assert.deepEqual(parsePriceMap('1k=0.06,2k=0.10,4k=0.20'), {
    ok: true,
    map: { '1k': 0.06, '2k': 0.1, '4k': 0.2 },
    error: '',
  })
  assert.deepEqual(parsePriceMap('{"1k":0,"2k":0.06,"4k":0.12}').map, {
    '1k': 0,
    '2k': 0.06,
    '4k': 0.12,
  })
})

test('historical order prices turn current-price differences into price drift warnings', () => {
  const currentPricing = choosePricingMap([{
    ok: true,
    url: 'https://dimilinks.com/api/pricing',
    resolution_options: [
      { resolution: '1k', model_price: 0 },
      { resolution: '2k', model_price: 0.06 },
      { resolution: '4k', model_price: 0.12 },
    ],
  }])
  const orderPricing = chooseOrderPricing('1k=0.06,2k=0.10,4k=0.20', currentPricing)

  const checks = buildChecks(
    {
      requested_resolution: '2k',
      resolution: '2k',
      status: 'success',
      task_id: 'img_old_2k',
      provider: 'codex',
      provider_channel: 'codex-cli-proxy-image',
      provider_trace_summary: 'codex(codex-cli-proxy-image)',
      local_db: { n: 1 },
    },
    [{
      task_id: 'task_old_2k',
      upstream_task_id: 'img_old_2k',
      billing_context: { resolution: '2k', model_price: 0.1 },
      result_count: 1,
    }],
    { current: currentPricing, order: orderPricing }
  )

  assert.equal(findCheck(checks, 'billing_context.model_price')?.level, 'pass')
  assert.equal(findCheck(checks, 'price_drift')?.level, 'warn')
  assert.match(findCheck(checks, 'price_drift')?.message || '', /价格漂移/)
  assert.equal(checks.some((check) => check.level === 'fail'), false)
})

test('without historical order prices, billing_context is checked against current prices', () => {
  const currentPricing = choosePricingMap([{
    ok: true,
    url: 'https://dimilinks.com/api/pricing',
    resolution_options: [
      { resolution: '1k', model_price: 0 },
      { resolution: '2k', model_price: 0.06 },
      { resolution: '4k', model_price: 0.12 },
    ],
  }])
  const orderPricing = chooseOrderPricing('', currentPricing)

  const checks = buildChecks(
    {
      requested_resolution: '2k',
      resolution: '2k',
      status: 'success',
      task_id: 'img_current_2k',
      provider: 'codex',
      local_db: { n: 1 },
    },
    [{
      task_id: 'task_current_2k',
      upstream_task_id: 'img_current_2k',
      billing_context: { resolution: '2k', model_price: 0.1 },
      result_count: 1,
    }],
    { current: currentPricing, order: orderPricing }
  )

  assert.equal(findCheck(checks, 'billing_context_mismatch')?.level, 'fail')
  assert.equal(findCheck(checks, 'price_drift'), undefined)
})

test('explicit order prices still fail real billing_context mismatches', () => {
  const currentPricing = choosePricingMap([])
  const orderPricing = chooseOrderPricing('1k=0,2k=0.06,4k=0.12', currentPricing)
  const checks = buildChecks(
    {
      requested_resolution: '4k',
      resolution: '4k',
      status: 'success',
      task_id: 'img_bad_4k',
      provider: 'codex',
      local_db: { n: 1 },
    },
    [{
      task_id: 'task_bad_4k',
      upstream_task_id: 'img_bad_4k',
      billing_context: { resolution: '4k', model_price: 0.2 },
      result_count: 1,
    }],
    { current: currentPricing, order: orderPricing }
  )

  const mismatch = findCheck(checks, 'billing_context_mismatch')
  assert.equal(mismatch?.level, 'fail')
  assert.match(mismatch?.message || '', /billing_context 真不一致/)
})

test('route anomalies are labeled separately from price drift and billing checks', () => {
  const currentPricing = choosePricingMap([])
  const orderPricing = chooseOrderPricing('1k=0,2k=0.06,4k=0.12', currentPricing)
  const checks = buildChecks(
    {
      requested_resolution: '2k',
      resolution: '2k',
      status: 'success',
      task_id: 'img_bad_route',
      provider: 'free_runner',
      fallback_triggered: false,
      local_db: { n: 1 },
    },
    [{
      task_id: 'task_bad_route',
      upstream_task_id: 'img_bad_route',
      billing_context: { resolution: '2k', model_price: 0.06 },
      result_count: 1,
    }],
    { current: currentPricing, order: orderPricing }
  )

  assert.equal(findCheck(checks, 'route_anomaly')?.level, 'fail')
  assert.equal(findCheck(checks, 'billing_context.model_price')?.level, 'pass')
})

function findCheck(checks, name) {
  return checks.find((check) => check.name === name)
}
