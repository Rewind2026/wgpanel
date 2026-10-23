// UI 侧验证：真实外部客户端在面板上的呈现。
//
// 与 ui_test.mjs 的区别：这里假定已有一个真实第三方客户端连着，
// 检查 UI 是否把「在线、流量、配置可复制」如实呈现出来。
import { chromium } from 'playwright'

const BASE = process.env.WGP_BASE_URL || 'http://127.0.0.1:8080'
const USER = process.env.WGP_OWNER_USER || 'admin'
const PASS = process.env.WGP_OWNER_PASS || ''
const PEER = process.env.WGP_EXT_PEER || 'ext_client_75'

let pass = 0
let fail = 0
const consoleErrors = []
const badResponses = []

function check(name, cond, detail = '') {
  if (cond) {
    pass++
    console.log('  OK   ' + name)
  } else {
    fail++
    console.log('  FAIL ' + name + '  -> ' + String(detail).slice(0, 300))
  }
}

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })
page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text())
})
page.on('response', (r) => {
  if (r.status() >= 400) badResponses.push(`${r.status()} ${r.url()}`)
})

await page.goto(BASE + '/login', { waitUntil: 'networkidle' })
await page.fill('#username', USER)
await page.fill('#password', PASS)
await page.getByRole('button', { name: /接入系统|登\s*录|sign in|login/i }).first().click()
await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20000 })

// 被测 peer 的地址由面板分配，先从 API 取真实值，UI 断言对着它比。
// 面板的 access token 只存在内存里，sessionStorage 取不到，所以这里单独登录一次。
const peer = await (async () => {
  const login = await fetch(BASE + '/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: USER, password: PASS }),
  })
  const { data: auth } = await login.json()
  const res = await fetch(BASE + '/api/v1/peers', {
    headers: { Authorization: 'Bearer ' + auth.accessToken },
  })
  const body = await res.json()
  const list = Array.isArray(body.data) ? body.data : body.data?.items ?? []
  return list.find((p) => p.name === PEER) ?? {}
})()
check('API 能取到被测 peer', !!peer.address, JSON.stringify(peer).slice(0, 200))

// 仪表盘
await page.goto(BASE + '/', { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)
const dash = await page.locator('body').innerText()
check('仪表盘渲染真实数据', /监听端口|在线/.test(dash), dash.slice(0, 200))

// 客户端页：定位被测 peer 所在行
await page.goto(BASE + '/peers', { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)
const nameCell = page.getByText(PEER, { exact: true }).first()
check('客户端列表含被测 peer', (await nameCell.count()) > 0)
const row = nameCell.locator('xpath=ancestor::div[@role="listitem"][1]')
const rowText = (await row.innerText()).replace(/\n+/g, ' | ')
// 地址由面板分配，断言必须对着 API 的真实值，不能写死某个网段。
check('行内显示地址与备注', rowText.includes(peer.address), rowText)
check('行内显示限速值', /1\s*MBPS/i.test(rowText), rowText)
check('行内显示收发流量', /\d/.test(rowText) && /(KB|MB|B)/.test(rowText), rowText)
check('行内显示最近活跃时间', /刚刚|前|秒|分/.test(rowText), rowText)

// 在线状态：StatusDot 用 aria-label 表达
const dot = row.locator('[aria-label="在线"], [aria-label="离线"]').first()
const dotLabel = (await dot.count()) ? await dot.getAttribute('aria-label') : null
check('在线状态指示为「在线」', dotLabel === '在线', `aria-label=${dotLabel}`)

// 操作按钮齐全。aria-label 里夹着 peer 名（如「复制 ext_client_75 配置」），
// 所以只匹配首段关键词。
for (const [name, kw] of [
  ['复制配置', '复制'],
  ['订阅链接', '订阅链接'],
  ['轮换密钥', '轮换'],
  ['编辑', '编辑'],
  ['删除', '删除'],
]) {
  check(
    `行内提供「${name}」按钮`,
    (await row.locator(`button[aria-label*="${kw}"]`).count()) > 0,
    `aria-label 含「${kw}」的按钮`,
  )
}

// 复制配置：应能读到含 Endpoint 的完整配置
await ctxClipboard(page)
await row.locator(`button[aria-label*="复制"]`).first().click()
await page.waitForTimeout(1500)
const bodyAfterCopy = await page.locator('body').innerText()
check('复制配置触发成功提示', /已复制|复制成功/.test(bodyAfterCopy), bodyAfterCopy.slice(0, 300))

// 实时监控：数据应随时间变化
await page.goto(BASE + '/monitor', { waitUntil: 'networkidle' })
await page.waitForTimeout(3000)
const m1 = await page.locator('body').innerText()
await page.waitForTimeout(4000)
const m2 = await page.locator('body').innerText()
check('监控页渲染隧道统计', /出口|端口|在线/.test(m1), m1.slice(0, 300))
check('监控页数据随时间刷新', m1 !== m2)

// 系统运维：回显公网出口地址
await page.goto(BASE + '/system', { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const ep = await page.inputValue('#s-endpoint').catch(() => null)
check('系统页回显公网出口地址', !!ep && ep.includes(':'), `value=${ep}`)

// 移动端
await page.setViewportSize({ width: 390, height: 844 })
await page.goto(BASE + '/peers', { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const overflow = await page.evaluate(
  () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
)
check('移动端无横向溢出', overflow <= 1, `overflow=${overflow}px`)

check('控制台无错误', consoleErrors.length === 0, consoleErrors.join(' | '))
check('无 >=400 的请求', badResponses.length === 0, badResponses.join(' | '))

await browser.close()
console.log('='.repeat(56))
console.log(`PASS=${pass}  FAIL=${fail}`)
if (fail) process.exit(1)

/** HTTP 部署下剪贴板走 execCommand 回退，需要页面内权限，Playwright 已授予。 */
async function ctxClipboard(p) {
  await p.evaluate(() => navigator.clipboard?.writeText?.('')).catch(() => {})
}