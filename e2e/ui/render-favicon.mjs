// 把 web/public/favicon.svg 光栅化成 PNG/ICO 各尺寸。
//
// 放在 e2e/ui 下是因为这里已经声明了 playwright 依赖，而 web/ 不需要
// 为了一个资源生成脚本背上 Chromium。
//
// 用法：cd e2e/ui && node render-favicon.mjs
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

const here = path.dirname(fileURLToPath(import.meta.url))
const publicDir = path.resolve(here, '../../web/public')
const svg = readFileSync(path.join(publicDir, 'favicon.svg'), 'utf8')

// favicon 常见取用尺寸；ICO 内嵌 16/32/48，PNG 供现代浏览器与移动端。
const sizes = [16, 32, 48, 64, 128, 180, 192, 512]

const browser = await chromium.launch()
const page = await browser.newPage()

const pngs = new Map()
for (const size of sizes) {
  // 用 data URI 直接内联 SVG，避免引入额外文件读取。
  const uri = `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`
  await page.setViewportSize({ width: size, height: size })
  await page.setContent(
    `<!doctype html><html><body style="margin:0;background:transparent">
       <img src="${uri}" width="${size}" height="${size}" style="display:block" />
     </body></html>`,
  )
  // 等图片解码完成再截，否则可能截到空白。
  await page.waitForFunction(() => {
    const img = document.querySelector('img')
    return img && img.complete && img.naturalWidth > 0
  })
  const buf = await page.screenshot({ omitBackground: true })
  pngs.set(size, buf)
  console.log(`PNG ${size}x${size} — ${buf.length} 字节`)
}

await browser.close()

mkdirSync(path.join(publicDir, 'favicon'), { recursive: true })
for (const [size, buf] of pngs) {
  writeFileSync(path.join(publicDir, 'favicon', `favicon-${size}.png`), buf)
}

// ---- 组装 ICO ----
// ICO 容器：6 字节头 + 16 字节目录项 × N + 各图像数据。
// Vista 起 ICO 允许直接内嵌 PNG，因此无需自己编码 BMP。
function buildIco(entries) {
  const count = entries.length
  const header = Buffer.alloc(6)
  header.writeUInt16LE(0, 0) // reserved
  header.writeUInt16LE(1, 2) // type = icon
  header.writeUInt16LE(count, 4)

  const dir = Buffer.alloc(16 * count)
  let offset = 6 + 16 * count
  entries.forEach((e, i) => {
    const o = i * 16
    dir.writeUInt8(e.size >= 256 ? 0 : e.size, o + 0) // width（256 记作 0）
    dir.writeUInt8(e.size >= 256 ? 0 : e.size, o + 1) // height
    dir.writeUInt8(0, o + 2) // 调色板数
    dir.writeUInt8(0, o + 3) // reserved
    dir.writeUInt16LE(1, o + 4) // color planes
    dir.writeUInt16LE(32, o + 6) // bits per pixel
    dir.writeUInt32LE(e.data.length, o + 8)
    dir.writeUInt32LE(offset, o + 12)
    offset += e.data.length
  })

  return Buffer.concat([header, dir, ...entries.map((e) => e.data)])
}

const ico = buildIco([16, 32, 48].map((size) => ({ size, data: pngs.get(size) })))
writeFileSync(path.join(publicDir, 'favicon.ico'), ico)
console.log(`ICO — ${ico.length} 字节（含 16/32/48）`)

// ---- apple-touch-icon ----
// iOS 会给图标加圆角遮罩，这里输出 180x180 的 PNG。
writeFileSync(path.join(publicDir, 'apple-touch-icon.png'), pngs.get(180))
console.log('apple-touch-icon.png — 180x180')
