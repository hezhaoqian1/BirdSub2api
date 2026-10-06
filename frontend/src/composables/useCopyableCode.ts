import { useClipboard } from './useClipboard'

/**
 * Markdown 内容中的行内代码（`xxx`）点击即复制。
 * 绑定到 v-html 容器的 @click 上，通过事件委托处理，代码块（pre code）不受影响。
 */
export function useCopyableCode() {
  const { copyToClipboard } = useClipboard()

  const handleCopyableCodeClick = (event: MouseEvent) => {
    const target = event.target as HTMLElement | null
    const code = target?.closest('code')
    if (!code || code.closest('pre')) return
    const text = code.textContent?.trim()
    if (text) void copyToClipboard(text)
  }

  return { handleCopyableCodeClick }
}
