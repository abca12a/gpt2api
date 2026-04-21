<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()

const STORAGE_KEY = 'gpt2api.oauth.openai.callback'
const MESSAGE_TYPE = 'gpt2api:openai-oauth-callback'

const code = computed(() => String(route.query.code || ''))
const state = computed(() => String(route.query.state || ''))
const error = computed(() => String(route.query.error || ''))
const errorDescription = computed(() => String(route.query.error_description || ''))

function publishOAuthResult() {
  const payload = {
    type: MESSAGE_TYPE,
    callback_url: window.location.href,
    code: code.value,
    state: state.value,
    error: error.value,
    error_description: errorDescription.value,
    ts: Date.now(),
  }

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(payload))
  } catch {
    // noop
  }

  try {
    if (window.opener && !window.opener.closed) {
      window.opener.postMessage(payload, window.location.origin)
    }
  } catch {
    // noop
  }
}

onMounted(() => {
  publishOAuthResult()
  window.setTimeout(() => {
    try {
      window.close()
    } catch {
      // noop
    }
  }, 1200)
})
</script>

<template>
  <div class="oauth-callback-page">
    <el-card class="callback-card" shadow="hover">
      <template v-if="error">
        <div class="title">OpenAI 登录失败</div>
        <div class="desc">{{ errorDescription || error }}</div>
        <div class="hint">结果已经回传到管理页，返回原页面后可重新发起授权。</div>
      </template>
      <template v-else>
        <div class="title">OpenAI 登录完成</div>
        <div class="desc">授权结果已回传到管理页，这个窗口会自动关闭。</div>
        <div class="hint">如果没有自动关闭，直接返回账号池导入弹窗继续即可。</div>
      </template>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
.oauth-callback-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background:
    radial-gradient(900px 360px at 10% 10%, #d7e9ff, transparent),
    radial-gradient(900px 360px at 90% 90%, #dff7df, transparent),
    linear-gradient(135deg, #f5f9ff, #fbfff8);
}

.callback-card {
  width: min(520px, 100%);
}

.title {
  font-size: 24px;
  font-weight: 700;
  color: #1f2937;
  margin-bottom: 10px;
}

.desc {
  color: #374151;
  line-height: 1.7;
}

.hint {
  margin-top: 12px;
  color: #6b7280;
  font-size: 13px;
}
</style>
