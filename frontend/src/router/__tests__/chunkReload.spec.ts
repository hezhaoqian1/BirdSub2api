import { describe, expect, it, vi } from 'vitest'
import {
  CHUNK_RELOAD_COOLDOWN_MS,
  CHUNK_RELOAD_KEY,
  isChunkLoadError,
  recoverFromChunkLoadError,
  type ChunkReloadEnv
} from '../chunkReload'

function makeEnv(initial: Record<string, string> = {}, now = 1_000_000) {
  const store = new Map(Object.entries(initial))
  const env: ChunkReloadEnv = {
    storage: {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => {
        store.set(key, value)
      }
    },
    now: () => now,
    assign: vi.fn(),
    reload: vi.fn()
  }
  return { env, store }
}

describe('isChunkLoadError', () => {
  it.each([
    'Failed to fetch dynamically imported module: https://x/assets/ChannelStatusView-abc.js',
    'error loading dynamically imported module: https://x/assets/a.js',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/ChannelStatusView-abc.css',
    'Loading chunk 12 failed.',
    'Loading CSS chunk 3 failed.'
  ])('识别浏览器/Vite 的分片加载错误: %s', (message) => {
    expect(isChunkLoadError(new TypeError(message))).toBe(true)
  })

  it('识别 ChunkLoadError 名称', () => {
    const err = new Error('x')
    err.name = 'ChunkLoadError'
    expect(isChunkLoadError(err)).toBe(true)
  })

  it('普通错误、空值不算分片错误', () => {
    expect(isChunkLoadError(new Error('Request failed with status code 500'))).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
    expect(isChunkLoadError('Failed to fetch dynamically imported module')).toBe(false)
  })
})

describe('recoverFromChunkLoadError', () => {
  const chunkError = new TypeError('Failed to fetch dynamically imported module: /assets/ChannelStatusView-old.js')

  it('整页跳到目标页而不是刷新来源页', () => {
    const { env, store } = makeEnv()
    expect(recoverFromChunkLoadError(chunkError, '/monitor', env)).toBe('navigate')
    expect(env.assign).toHaveBeenCalledWith('/monitor')
    expect(env.reload).not.toHaveBeenCalled()
    expect(store.get(CHUNK_RELOAD_KEY)).toBe('1000000')
  })

  it('拿不到目标页时退回刷新当前页', () => {
    const { env } = makeEnv()
    expect(recoverFromChunkLoadError(chunkError, null, env)).toBe('reload')
    expect(env.reload).toHaveBeenCalledTimes(1)
    expect(env.assign).not.toHaveBeenCalled()
  })

  it('冷却期内不再重复跳转，避免死循环', () => {
    const now = 1_000_000
    const { env } = makeEnv({ [CHUNK_RELOAD_KEY]: String(now - 3_000) }, now)
    expect(recoverFromChunkLoadError(chunkError, '/monitor', env)).toBe('suppressed')
    expect(env.assign).not.toHaveBeenCalled()
    expect(env.reload).not.toHaveBeenCalled()
  })

  it('冷却期过后允许再次跳转', () => {
    const now = 1_000_000
    const { env } = makeEnv({ [CHUNK_RELOAD_KEY]: String(now - CHUNK_RELOAD_COOLDOWN_MS - 1) }, now)
    expect(recoverFromChunkLoadError(chunkError, '/monitor', env)).toBe('navigate')
  })

  it('sessionStorage 不可用时照样跳转', () => {
    const { env } = makeEnv()
    env.storage = {
      getItem: () => {
        throw new Error('SecurityError')
      },
      setItem: () => {
        throw new Error('SecurityError')
      }
    }
    expect(recoverFromChunkLoadError(chunkError, '/monitor', env)).toBe('navigate')
    expect(env.assign).toHaveBeenCalledWith('/monitor')
  })

  it('非分片错误不处理', () => {
    const { env } = makeEnv()
    expect(recoverFromChunkLoadError(new Error('boom'), '/monitor', env)).toBe('ignored')
    expect(env.assign).not.toHaveBeenCalled()
    expect(env.reload).not.toHaveBeenCalled()
  })
})
