'use strict'
// URLs the web UI may ask the desktop shell to open in the system browser: the OpenRouter login page ("sign in with
// <platform>") and the API-key pages (preset key_url) of the shipped LLM presets, so a novice gets there with one click.
// https only, exact host allow-list, no credentials, no non-default port.
// Keep in sync with backend/internal/llm/oauth.go, backend/internal/llm/presets.json (key_url; test/external-url.test.js
// fails when they drift) and docs/LLM账号登录配置.md.
const ALLOWED_HOSTS = new Set([
  'openrouter.ai', // login flow + key page
  'platform.deepseek.com', // DeepSeek key page
  'bailian.console.aliyun.com', // Qwen / Alibaba Bailian key page
  'platform.openai.com', // OpenAI key page
])

function checkExternalUrl(raw, hosts = ALLOWED_HOSTS) {
  if (typeof raw !== 'string' || raw.length > 4096) return null
  let u
  try {
    u = new URL(raw)
  } catch {
    return null
  }
  if (u.protocol !== 'https:' || u.username || u.password || u.port) return null
  if (!hosts.has(u.hostname.toLowerCase())) return null
  return u.href
}

module.exports = { checkExternalUrl, ALLOWED_HOSTS }
