/**
 * P2 测量：切换菜单时 SSE 连接的数量与噪声。
 *
 * 关注三件事：
 *  1. 每切一次菜单，EventSource 被销毁重建的次数
 *  2. Console 里 net::ERR_ABORTED 的条数
 *  3. /api/v1/metrics/stream 请求的条数
 *
 * 凭据从环境变量读取，不落盘。
 */
import { chromium } from 'playwright'

const BASE = process.env.WGP_BASE_URL || 'http://192.168.50.63:8080'
const USER = process.env.WGP_OWNER_USER || 'admin'
const PASS = process.env.WGP_OWNER_PASS

if (!PASS) {
  console.error('缺少环境变量 WGP_OWNER_PASS')
  process.exit(2)
}

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
const page = await ctx.newPage()

const consoleErrors = []
const streamRequests = []
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()) })
page.on('pageerror', (e) => consoleErrors.push('PAGEERROR: ' + e.message))
page.on('request', (r) => {
  if (r.url().includes('/api/v1/metrics/stream')) streamRequests.push(r.url())
})
page.on('requestfailed', (r) => {
  if (r.url().includes('/api/v1/metrics/stream')) {
    consoleErrors.push('REQFAILED: ' + r.url().split('?')[0] + ' :: ' + (r.failure()?.errorText || ''))
  }
})

await page.goto(BASE, { waitUntil: 'networkidle' })
await page.fill('input[name="username"], input#username', USER)
await page.fill('input[type="password"]', PASS)
await page.getByRole('button', { name: /接入系统|登\s*录/i }).first().click()
await page.waitForTimeout(2500)

const menu = (label) => page.getByRole('link', { name: new RegExp(label) }).first()

console.log('=== 基线：进入仪表盘（开一条 SSE）===')
await menu('仪表盘').click()
await page.waitForTimeout(2500)
console.log('  stream 请求数:', streamRequests.length)
console.log('  console error 数:', consoleErrors.length)

console.log('=== 切到实时监控 ===')
await menu('实时监控').click()
await page.waitForTimeout(2500)
console.log('  stream 请求数:', streamRequests.length)

console.log('=== 连续切换 6 次（每页停 1.5s）===')
const cycle = ['客户端', '审计日志', '系统运维', '服务端设置', '账号', '仪表盘']
for (const label of cycle) {
  await menu(label).click()
  await page.waitForTimeout(1500)
}
console.log('  stream 请求总数:', streamRequests.length)
console.log('  console error 总数:', consoleErrors.length)

const aborted = consoleErrors.filter((e) => /ERR_ABORTED|REQFAILED/.test(e))
const jsErrors = consoleErrors.filter((e) => !/ERR_ABORTED|REQFAILED/.test(e))
console.log('  其中 ERR_ABORTED/REQFAILED:', aborted.length)
console.log('  其中真正的 JS 错误:', jsErrors.length)

console.log('=== 样本 ===')
aborted.slice(0, 3).forEach((e) => console.log('  - ' + e.slice(0, 150)))
jsErrors.slice(0, 5).forEach((e) => console.log('  ! ' + e.slice(0, 250)))

console.log('=== SSE 连接存活检查 ===')
await page.waitForTimeout(3000)
const badge = await page.evaluate(() => {
  const m = document.body.innerText.match(/实时推送[^\n]*/g)
  return m || []
})
console.log('  状态徽章:', JSON.stringify(badge))

await browser.close()
console.log('SUMMARY stream_requests=' + streamRequests.length + ' aborted=' + aborted.length + ' js_errors=' + jsErrors.length)
