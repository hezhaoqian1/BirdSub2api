import type { OpsRequestSnapshot } from '@/api/admin/ops'

export type RequestSnapshotFieldKey =
  | 'size'
  | 'format'
  | 'model'
  | 'stream'
  | 'max_tokens'
  | 'message_count'
  | 'system_chars'
  | 'tool_count'
  | 'tool_names'
  | 'thinking'
  | 'reasoning'

export interface RequestSnapshotRow {
  key: RequestSnapshotFieldKey
  value: string
}

/** 解析错误详情中的 request_snapshot（JSON 字符串）；缺省或格式不对时返回 null。 */
export function parseRequestSnapshot(raw?: string | null): OpsRequestSnapshot | null {
  const value = String(raw || '').trim()
  if (!value) return null
  try {
    const parsed = JSON.parse(value) as OpsRequestSnapshot
    if (!parsed || typeof parsed !== 'object' || !parsed.summary) return null
    return parsed
  } catch {
    return null
  }
}

function stringifyCompact(value: unknown): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

/** 摘要的展示行，只保留有值的字段。size 由调用方格式化。 */
export function requestSnapshotRows(
  snapshot: OpsRequestSnapshot,
  formatSize: (bytes: number) => string,
  formatBool: (value: boolean) => string
): RequestSnapshotRow[] {
  const s = snapshot.summary
  const rows: RequestSnapshotRow[] = [
    { key: 'size', value: formatSize(s.size_bytes || 0) },
    { key: 'format', value: s.format || '' },
    { key: 'model', value: s.model || '' },
    { key: 'stream', value: typeof s.stream === 'boolean' ? formatBool(s.stream) : '' },
    { key: 'max_tokens', value: s.max_tokens != null ? String(s.max_tokens) : '' },
    { key: 'message_count', value: s.message_count != null ? String(s.message_count) : '' },
    { key: 'system_chars', value: s.system_chars != null ? String(s.system_chars) : '' },
    { key: 'tool_count', value: s.tool_count != null ? String(s.tool_count) : '' },
    { key: 'tool_names', value: (s.tool_names || []).join(', ') },
    { key: 'thinking', value: stringifyCompact(s.thinking) },
    { key: 'reasoning', value: stringifyCompact(s.reasoning) }
  ]
  return rows.filter(row => row.value !== '')
}

/**
 * 复制用文本：完整请求体原样返回（可直接重放）；被截断时拼接首尾片段并标注
 * 省略字节数（此时不是合法 JSON）。二进制请求体无正文可复制。
 */
export function requestSnapshotCopyText(snapshot: OpsRequestSnapshot, omittedMarker: string): string {
  if (snapshot.body) return snapshot.body
  if (!snapshot.head && !snapshot.tail) return ''
  return `${snapshot.head || ''}\n\n${omittedMarker}\n\n${snapshot.tail || ''}`
}
