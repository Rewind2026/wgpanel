/**
 * UI 深度交互测试：在页面上真实操作，验证前后端闭环。
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'node:fs'

const BASE = process.env.WGP_BASE_URL || 'http://127.0.0.1:8080'
const OUT = process.argv[2] || '/tmp/wgp_ui_int'
mkdirSync(OUT, { recursive: true })

let pass = 0, fail = 0
const consoleErrors = []
function check(name, ok, detail = '') {
  if (ok) { pass++; console.log('  OK   ' + name) }
  else { fail++; console.log('  FAIL ' + name + '  -> ' + detail) }
}

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
const page = await ctx.newPage()
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()) })
page.on('pageerror', (e) => consoleErrors.push(e.message))
const shot = (n) => page.screenshot({ path: OUT + '/' + n + '.png', fullPage: true })

// 登录
await page.goto(BASE, { waitUntil: 'networkidle' })
await page.fill('input[name="username"], input#username', (process.env.WGP_OWNER_USER || 'admin'))
await page.fill('input[type="password"]', (process.env.WGP_OWNER_PASS || ''))
await page.getByRole('button', { name: /接入系统|登\s*录/i }).first().click()
await page.waitForTimeout(2000)

console.log('== A. 页面内创建客户端 ==')
await page.goto(BASE + '/peers', { waitUntil: 'networkidle' })
await page.waitForTimeout(1000)

const before = await page.locator('[role="listitem"]').count()
console.log('  现有客户端数:', before)

await page.getByRole('button', { name: /新建客户端|新\s*建客户端/i }).first().click()
await page.waitForTimeout(800)
await shot('A1-dialog')

// 填名称
await page.locator('#peer-name').fill('ui_interact_test')
await page.locator('#peer-note').fill('UI 交互测试')
await page.locator('#peer-tags').fill('ui-test')
await page.keyboard.press('Enter')
await shot('A2-filled')

await page.locator('[role="dialog"] button', { hasText: /^\s*保\s*存\s*$/ }).last().click()
await page.waitForTimeout(2500)
await shot('A3-created')
const after = await page.locator('[role="listitem"]').count()
check('UI 创建客户端成功', after === before + 1, `before=${before} after=${after}`)

const row = page.locator('[role="listitem"]').filter({ hasText: 'ui_interact_test' }).first()
check('新客户端出现在列表', (await row.count()) > 0, 'not found')

console.log('== B. 搜索过滤 ==')
const search = page.locator('input[placeholder*="搜索"], input[placeholder*="按名称"]').first()
if (await search.count()) {
  await search.fill('ui_interact_test')
  await page.waitForTimeout(900)
  const visible = await page.locator('[role="listitem"]').count()
  check('搜索过滤生效', visible === 1, 'visible=' + visible)
  await shot('B1-search')
  await search.fill('')
  await page.waitForTimeout(600)
}

console.log('== C. 批量启停 ==')
await page.locator('input[aria-label="选择 ui_interact_test"]').check()
await page.waitForTimeout(600)
const bulkDisable = page.getByRole('button', { name: /批量停用/i }).first()
check('批量停用按钮出现', (await bulkDisable.count()) > 0, 'not found')
await bulkDisable.click()
await page.waitForTimeout(2500)
await shot('C1-disabled')
let stateTxt = await row.innerText()
check('停用后显示已停用标记', /已停用/.test(stateTxt), stateTxt.slice(0, 150))
// 重新启用
await page.locator('input[aria-label="选择 ui_interact_test"]').check()
await page.waitForTimeout(500)
await page.getByRole('button', { name: /批量启用/i }).first().click()
await page.waitForTimeout(2500)
stateTxt = await row.innerText()
check('重新启用后不再显示已停用', !/已停用/.test(stateTxt), stateTxt.slice(0, 150))
await page.locator('input[aria-label="选择 ui_interact_test"]').uncheck()
await page.waitForTimeout(500)

console.log('== D. 编辑弹窗 ==')
const editBtn = row.getByRole('button', { name: /编辑|修\s*改/i }).first()
if (await editBtn.count()) {
  await editBtn.click()
  await page.waitForTimeout(900)
  await shot('D1-edit')
  const noteField = page.locator('#peer-note')
  if (await noteField.count()) {
    await noteField.fill('UI 交互测试-已改')
    await page.locator('[role="dialog"] button', { hasText: /^\s*保\s*存\s*$/ }).last().click()
    await page.waitForTimeout(2000)
    const txt = await row.innerText()
    check('编辑保存生效', txt.includes('已改'), txt.slice(0, 120))
  } else {
    check('编辑弹窗有备注字段', false, 'not found')
  }
} else {
  check('编辑按钮存在', false, 'not found')
}

console.log('== E. 复制配置 ==')
const copyBtn = row.getByRole('button', { name: /复制|配置/i }).first()
if (await copyBtn.count()) {
  await copyBtn.click()
  await page.waitForTimeout(2000)
  await shot('E1-copy')
  const toastText = await page.evaluate(() => {
    const t = document.body.innerText
    const m = t.match(/[^\n]*(复制|剪贴板)[^\n]*/g)
    return m || []
  })
  console.log('  复制反馈:', JSON.stringify(toastText))
  check('复制配置后有反馈', toastText.some((t) => /复制|剪贴板/.test(t)), JSON.stringify(toastText))
} else {
  check('复制配置按钮存在', false, 'not found')
}

console.log('== F. 删除客户端 ==')
const delBtn = row.getByRole('button', { name: /删\s*除|移除/i }).first()
if (await delBtn.count()) {
  await delBtn.click()
  await page.waitForTimeout(800)
  await shot('F1-confirm')
  const confirmBtn = page.getByRole('button', { name: /确\s*认|删\s*除|确定/i }).last()
  await confirmBtn.click()
  await page.waitForTimeout(2000)
  await shot('F2-deleted')
  const final = await page.locator('[role="listitem"]').count()
  check('UI 删除客户端成功', final === before, `before=${before} final=${final}`)
} else {
  check('删除按钮存在', false, 'not found')
}

console.log('== G. 审计页应记录操作 ==')
await page.goto(BASE + '/audit', { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
await shot('G1-audit')
const auditText = await page.locator('body').innerText()
check('审计页出现 peer 操作记录', /peer|客户端|ui_interact/i.test(auditText), auditText.slice(0, 300))

console.log('== H. 监控页 SSE 表格 ==')
await page.goto(BASE + '/monitor', { waitUntil: 'networkidle' })
await page.waitForTimeout(3000)
await shot('H1-monitor')
const sseBadge = await page.evaluate(() => {
  const t = document.body.innerText
  const m = t.match(/实时推送[^\n]*/g)
  return m || []
})
console.log('  SSE 状态徽章:', JSON.stringify(sseBadge))
check('SSE 显示已连接', sseBadge.some((t) => /已连接/.test(t)), JSON.stringify(sseBadge))

console.log('== J. 退出登录 ==')
const logout = page.getByRole('button', { name: /退出登录|登\s*出/i }).first()
if (await logout.count()) {
  await logout.click()
  await page.waitForTimeout(1500)
  check('退出后回到登录页', page.url().includes('/login'), page.url())
  await shot('J1-logout')
} else {
  check('退出登录按钮存在', false, 'not found')
}

console.log('== 控制台错误 ==')
console.log('  errors:', consoleErrors.length)
consoleErrors.slice(0, 10).forEach((e) => console.log('   - ' + e.slice(0, 200)))
check('无控制台错误', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | ').slice(0, 200))

await browser.close()
console.log('='.repeat(60))
console.log('PASS=' + pass + '  FAIL=' + fail)
process.exit(fail ? 1 : 0)
