import { useState, useEffect } from 'react'
import { onMessage } from '../ws'
import type { PreviewUpdated } from '../types'

export function usePreview() {
  const [html, setHtml] = useState('')

  useEffect(() => {
    const unsub = onMessage<PreviewUpdated>('preview_updated', (msg) => {
      setHtml(msg.html)
    })
    return unsub
  }, [])

  return { html }
}
