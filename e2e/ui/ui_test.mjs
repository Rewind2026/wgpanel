/**
 * UI 验证脚本：登录 → 逐页截图 → 交互检查 → 收集控制台错误。
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'node:fs'

const BASE = process.env.WGP_BASE_URL || 'http://127.0.0.1:8080'
const OUT = process.argv[2] || '/tmp/wgp_ui'
const USER = (process.env.WGP_OWNER_USER || 'admin')
const PASS = process.env.WGP_OWNER_PASS || ''

mkdirSync(OUT, { recursive: true })

const consoleErrors = []
const netErrors = []
let pass = 0
let fail = 0

function check(name, ok, detail = '') {
  if (ok) { pass++; console.log('  OK   ' + name) }
  else { fail++; console.log('  FAIL ' + name + '  -> ' + detail) }
}

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
const page = await ctx.newPage()

page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(page.url() + ' :: ' + m.text())
})
page.on('pageerror', (e) => consoleErrors.push(page.url() + ' :: ' + e.message))
page.on('response', (r) => {
  if (r.status() >= 400) netErrors.push(r.status() + ' ' + r.request().method() + ' ' + r.url())
})

async function shot(name) {
  await page.screenshot({ path: OUT + '/' + name + '.png', fullPage: true })
  console.log('  .. screenshot ' + name + '.png')
}

console.log('== 1. 登录页 ==')
await page.goto(BASE, { waitUntil: 'networkidle' })
await shot('01-login')
check('登录页渲染', (await page.locator('input[type="password"]').count()) > 0)

console.log('== 2. 登录 ==')
await page.fill('input[name="username"], input#username', USER)
await page.fill('input[type="password"]', PASS)
await page.getByRole('button', { name: /接入系统|登\s*录|sign in|login/i }).first().click()
await page.waitForTimeout(2500)
check('登录成功并跳转', !page.url().includes('/login'), page.url())
await shot('02-dashboard')

console.log('== 3. 仪表盘 ==')
const dashText = await page.locator('body').innerText()
check('仪表盘有内容', dashText.length > 100, 'len=' + dashText.length)
check('仪表盘显示在线客户端', /在线|ONLINE/i.test(dashText), dashText.slice(0, 200))

console.log('== 4. 客户端页 ==')
await page.goto(BASE + '/peers', { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
await shot('03-peers')
const peersText = await page.locator('body').innerText()
check('客户端页渲染', peersText.length > 50, 'len=' + peersText.length)

const gapInfo = await page.evaluate(() => {
  const items = document.querySelectorAll('[role="listitem"]')
  if (items.length === 0) return null
  const item = items[0]
  return Array.from(item.children).map((el) => {
    const r = el.getBoundingClientRect()
    return {
      cls: (el.className || '').toString().slice(0, 50),
      left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width),
      text: (el.innerText || '').replace(/\n/g, ' | ').slice(0, 40),
    }
  })
})
if (gapInfo) {
  console.log('  客户端行布局: ' + JSON.stringify(gapInfo))
  const vis = gapInfo.filter((k) => k.width > 0)
  let minGap = Infinity
  for (let i = 1; i < vis.length; i++) minGap = Math.min(minGap, vis[i].left - vis[i - 1].right)
  check('数值列之间有可见间距', minGap >= 8, 'minGap=' + minGap + 'px')
} else {
  console.log('  (客户端列表为空，跳过间距检查)')
}

console.log('== 5. 客户端弹窗 ==')
const addBtn = page.getByRole('button', { name: /新\s*建|添加|add|create/i }).first()
if (await addBtn.count()) {
  await addBtn.click()
  await page.waitForTimeout(900)
  await shot('04-peer-dialog')
  const dialogText = await page.locator('body').innerText()
  check('新建客户端弹窗打开', /名称|地址|allowed/i.test(dialogText), dialogText.slice(0, 200))
  await page.keyboard.press('Escape')
  await page.waitForTimeout(500)
}

console.log('== 6. 监控页 ==')
await page.goto(BASE + '/monitor', { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)
await shot('05-monitor')
const monText = await page.locator('body').innerText()
check('监控页渲染', monText.length > 50, 'len=' + monText.length)
check('SSE 已连接或表格存在', /推送|在线|握手/.test(monText), monText.slice(0, 300))

console.log('== 7. 服务端设置页 ==')
await page.goto(BASE + '/settings', { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
await shot('06-settings')
const setText = await page.locator('body').innerText()
check('服务端设置页渲染', setText.length > 100, 'len=' + setText.length)
check('设置页含接口/网段等关键字段',
  /接口名|监听端口|接口地址|AllowedIPs|AllowedIPs|AllowedIps/i.test(setText),
  setText.slice(0, 200))

console.log('== 8. 账号页 ==')
await page.goto(BASE + '/account', { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
await shot('07-account')
const accText = await page.locator('body').innerText()
check('账号页渲染', accText.length > 50, 'len=' + accText.length)
check('账号页含修改密码入口', /修改密码|新密码/.test(accText), accText.slice(0, 200))

console.log('== 9. 审计页 ==')
await page.goto(BASE + '/audit', { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
await shot('08-audit')
check('审计页渲染', (await page.locator('body').innerText()).length > 50)

console.log('== 10. 系统运维页 ==')
await page.goto(BASE + '/system', { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
await shot('09-system')
const sysText = await page.locator('body').innerText()
check('系统运维页渲染', sysText.length > 100, 'len=' + sysText.length)
const sysTabs = await page.evaluate(() =>
  Array.from(document.querySelectorAll('button, a')).map((b) => (b.innerText || '').trim())
    .filter((t) => /备份|订阅|系统|日志|维护|关于/i.test(t)))
console.log('  系统页标签:', JSON.stringify(sysTabs))
for (const t of sysTabs) {
  const btn = page.getByRole('button', { name: t, exact: false }).first()
  if (await btn.count()) {
    await btn.click()
    await page.waitForTimeout(1200)
    await shot('10-system-' + t.replace(/[^\w\u4e00-\u9fa5]/g, '_'))
  }
}

console.log('== 12. 导航链接 ==')
await page.goto(BASE, { waitUntil: 'networkidle' })
await page.waitForTimeout(1000)
const navLinks = await page.evaluate(() =>
  Array.from(document.querySelectorAll('a[href]')).map((a) => a.getAttribute('href')).filter(Boolean))
console.log('  导航链接: ' + JSON.stringify(navLinks))

console.log('== 13. 移动端视口 ==')
await page.setViewportSize({ width: 390, height: 844 })
await page.goto(BASE + '/peers', { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
await shot('11-mobile-peers')
const overflow = await page.evaluate(() => ({
  scrollW: document.documentElement.scrollWidth,
  clientW: document.documentElement.clientWidth,
}))
check('移动端无横向溢出', overflow.scrollW <= overflow.clientW + 2, JSON.stringify(overflow))

console.log('== 14. 控制台 / 网络错误 ==')
console.log('  console errors: ' + consoleErrors.length)
consoleErrors.slice(0, 20).forEach((e) => console.log('   - ' + e))
console.log('  http >=400: ' + netErrors.length)
netErrors.slice(0, 20).forEach((e) => console.log('   - ' + e))
check('无控制台错误', consoleErrors.length === 0, consoleErrors.slice(0, 3).join(' | '))

await browser.close()
console.log('='.repeat(60))
console.log('PASS=' + pass + '  FAIL=' + fail)
console.log('截图目录: ' + OUT)
process.exit(fail ? 1 : 0)
