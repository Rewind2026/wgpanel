// 站点图标的浏览器端回归：验证图标资源、manifest、登录流程与页面渲染。
import { chromium } from 'playwright'

const BASE = process.env.WGP_BASE || 'http://127.0.0.1:18082'
const USER = process.env.WGP_USER || 'admin'
const PASS = process.env.WGP_PASS || 'TestPass123456'

let pass = 0
let fail = 0
function check(name, ok, detail = '') {
  if (ok) {
    pass++
    console.log(`  PASS  ${name}`)
  } else {
    fail++
    console.log(`  FAIL  ${name}${detail ? ' — ' + detail : ''}`)
  }
}

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })

const consoleErrors = []
const badResponses = []
page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text())
})
page.on('response', (r) => {
  if (r.status() >= 400) badResponses.push(`${r.status()} ${r.url()}`)
})

console.log('=== 1. 登录页 ===')
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
check('页面标题正确', (await page.title()) === 'WGPanel', await page.title())

console.log('=== 2. 图标声明 ===')
const links = await page.evaluate(() =>
  Array.from(document.querySelectorAll('link[rel*="icon"], link[rel="apple-touch-icon"], link[rel="manifest"]')).map(
    (l) => ({ rel: l.rel, href: l.getAttribute('href'), type: l.getAttribute('type') }),
  ),
)
check('声明了 SVG favicon', links.some((l) => l.href === '/favicon.svg' && l.type === 'image/svg+xml'))
check('声明了 ICO 兜底', links.some((l) => l.href === '/favicon.ico'))
check('声明了 PNG 兜底', links.some((l) => l.href === '/favicon/favicon-32.png'))
check('声明了 apple-touch-icon', links.some((l) => l.href === '/apple-touch-icon.png'))
check('声明了 manifest', links.some((l) => l.href === '/site.webmanifest'))

console.log('=== 3. 图标资源实际加载 ===')
const probe = await page.evaluate(async () => {
  const out = {}
  for (const l of document.querySelectorAll('link[rel*="icon"], link[rel="apple-touch-icon"], link[rel="manifest"]')) {
    const href = l.getAttribute('href')
    try {
      const r = await fetch(new URL(href, location.href))
      const b = await r.arrayBuffer()
      out[href] = { status: r.status, type: r.headers.get('content-type'), bytes: b.byteLength }
    } catch (e) {
      out[href] = { error: String(e) }
    }
  }
  return out
})
for (const [href, v] of Object.entries(probe)) {
  check(`${href} 可加载`, v.status === 200 && v.bytes > 0, JSON.stringify(v))
}
check(
  'manifest Content-Type 正确',
  probe['/site.webmanifest']?.type === 'application/manifest+json',
  String(probe['/site.webmanifest']?.type),
)
check('SVG favicon 类型正确', probe['/favicon.svg']?.type === 'image/svg+xml', String(probe['/favicon.svg']?.type))
check('ICO 类型正确', probe['/favicon.ico']?.type === 'image/vnd.microsoft.icon', String(probe['/favicon.ico']?.type))

console.log('=== 4. manifest 内容 ===')
const manifest = await page.evaluate(async () => {
  const r = await fetch('/site.webmanifest')
  return r.json()
})
check('manifest 可解析为 JSON', !!manifest.name, manifest.name)
check('manifest 含 192 图标', manifest.icons?.some((i) => i.sizes === '192x192'))
check('manifest 含 512 图标', manifest.icons?.some((i) => i.sizes === '512x512'))
check('manifest 含 SVG maskable', manifest.icons?.some((i) => i.purpose === 'maskable'))

console.log('=== 5. 登录流程 ===')
await page.fill('input[name="username"]', USER)
await page.fill('input[name="password"]', PASS)
await page.click('button[type="submit"]')
await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 15000 })
check('登录后跳转出 /login', !page.url().includes('/login'), page.url())
await page.waitForTimeout(1500)
check('侧边栏渲染出 WGPanel 字样', (await page.locator('text=WGPanel').count()) > 0)

console.log('=== 6. 各路由下的图标 href 保持根路径 ===')
for (const route of ['/peers', '/monitor', '/settings', '/account', '/audit', '/system']) {
  await page.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
  const hrefs = await page.evaluate(() =>
    Array.from(document.querySelectorAll('link[rel*="icon"]')).map((l) => l.getAttribute('href')),
  )
  const allRoot = hrefs.length > 0 && hrefs.every((h) => h.startsWith('/'))
  check(`${route} 图标路径未带偏`, allRoot, JSON.stringify(hrefs))
}

console.log('=== 7. 控制台与网络错误 ===')
check('无控制台错误', consoleErrors.length === 0, consoleErrors.join(' | '))
check('无 4xx/5xx 请求', badResponses.length === 0, badResponses.join(' | '))

await browser.close()
console.log(`\n合计：PASS=${pass} FAIL=${fail}`)
process.exit(fail === 0 ? 0 : 1)
