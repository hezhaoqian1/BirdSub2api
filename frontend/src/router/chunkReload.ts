/**
 * 发版后旧页面切路由时的"分片加载失败"恢复。
 *
 * 背景：前端打包进后端二进制，每次发版 assets/ 下的 hash 文件名整体换新，旧文件不再存在。
 * 已打开很久的标签页里，index.html 还指向旧 hash；点一个本次会话没加载过的懒加载路由时，
 * 浏览器去拿旧分片，后端对不存在的路径回退返回 index.html（text/html + nosniff），
 * 于是 import() 失败，路由跳转被中止。
 *
 * 旧逻辑在这里 window.location.reload() —— 但此时 URL 还停在"来源页"，
 * 结果是整页白屏刷新后又回到原页面（例如在「使用记录」点「渠道状态」→ 白屏 → 回到使用记录）。
 * 现在改为整页跳到"目标页"，拿到新版 index.html 后直接落在用户想去的页面。
 */

const CHUNK_ERROR_PATTERNS = [
  'Failed to fetch dynamically imported module', // Chromium
  'error loading dynamically imported module', // Firefox
  'Importing a module script failed', // Safari
  'Unable to preload CSS', // Vite 预加载 CSS 依赖失败
  'Loading chunk', // webpack 风格（保留兼容）
  'Loading CSS chunk'
]

export const CHUNK_RELOAD_KEY = 'chunk_reload_attempted'
export const CHUNK_RELOAD_COOLDOWN_MS = 10_000

export function isChunkLoadError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const { name, message } = error as { name?: unknown; message?: unknown }
  if (name === 'ChunkLoadError') return true
  if (typeof message !== 'string') return false
  return CHUNK_ERROR_PATTERNS.some((pattern) => message.includes(pattern))
}

export interface ChunkReloadEnv {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  now: () => number
  /** 整页跳转到 href（目标页）。 */
  assign: (href: string) => void
  /** 拿不到目标页时刷新当前页。 */
  reload: () => void
}

/**
 * 处理一次路由错误。返回值：'navigate' / 'reload' 表示已触发整页加载，
 * 'suppressed' 表示冷却期内不再重试（避免死循环），'ignored' 表示不是分片错误。
 */
export function recoverFromChunkLoadError(
  error: unknown,
  targetHref: string | null | undefined,
  env: ChunkReloadEnv
): 'navigate' | 'reload' | 'suppressed' | 'ignored' {
  if (!isChunkLoadError(error)) return 'ignored'

  const now = env.now()
  let last = NaN
  try {
    last = Number.parseInt(env.storage?.getItem(CHUNK_RELOAD_KEY) ?? '', 10)
  } catch {
    // 隐私模式等场景下 sessionStorage 可能不可用，按"从未重试过"处理
  }
  if (Number.isFinite(last) && now - last <= CHUNK_RELOAD_COOLDOWN_MS) {
    return 'suppressed'
  }
  try {
    env.storage?.setItem(CHUNK_RELOAD_KEY, String(now))
  } catch {
    // 同上，写不进去也继续；最坏情况是失去防循环保护，但目标页 index.html 是 no-cache，会拿到新版本
  }

  if (targetHref) {
    env.assign(targetHref)
    return 'navigate'
  }
  env.reload()
  return 'reload'
}
