import { describe, expect, it } from 'vitest'
import { parseRequestSnapshot, requestSnapshotCopyText, requestSnapshotRows } from '../requestSnapshot'

describe('requestSnapshot utils', () => {
  it('returns null for missing or malformed snapshots', () => {
    expect(parseRequestSnapshot(undefined)).toBeNull()
    expect(parseRequestSnapshot('')).toBeNull()
    expect(parseRequestSnapshot('not json')).toBeNull()
    expect(parseRequestSnapshot('{"truncated":false}')).toBeNull()
  })

  it('builds summary rows for present fields only', () => {
    const snapshot = parseRequestSnapshot(
      JSON.stringify({
        summary: {
          size_bytes: 2048,
          format: 'json',
          model: 'claude-sonnet-4-6',
          stream: false,
          max_tokens: 20000,
          tool_names: ['a', 'b'],
          thinking: { type: 'enabled' }
        },
        truncated: false,
        body: '{}'
      })
    )!
    const rows = requestSnapshotRows(snapshot, bytes => `${bytes}B`, v => (v ? 'yes' : 'no'))
    expect(rows.map(r => r.key)).toEqual(['size', 'format', 'model', 'stream', 'max_tokens', 'tool_names', 'thinking'])
    expect(rows.find(r => r.key === 'size')?.value).toBe('2048B')
    expect(rows.find(r => r.key === 'stream')?.value).toBe('no')
    expect(rows.find(r => r.key === 'tool_names')?.value).toBe('a, b')
    expect(rows.find(r => r.key === 'thinking')?.value).toBe('{"type":"enabled"}')
  })

  it('copies the full body verbatim, or head + marker + tail when truncated', () => {
    const full = parseRequestSnapshot(JSON.stringify({ summary: { size_bytes: 2, format: 'json' }, truncated: false, body: '{}' }))!
    expect(requestSnapshotCopyText(full, 'MARK')).toBe('{}')

    const cut = parseRequestSnapshot(
      JSON.stringify({ summary: { size_bytes: 9, format: 'json' }, truncated: true, head: 'HEAD', tail: 'TAIL', omitted_bytes: 1 })
    )!
    expect(requestSnapshotCopyText(cut, 'MARK')).toBe('HEAD\n\nMARK\n\nTAIL')

    const binary = parseRequestSnapshot(JSON.stringify({ summary: { size_bytes: 9, format: 'binary' }, truncated: true }))!
    expect(requestSnapshotCopyText(binary, 'MARK')).toBe('')
  })
})
