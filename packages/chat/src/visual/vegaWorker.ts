import { drawChart } from './vegaRuntime'
import { ChartRefused } from './vegaPolicy'
import { HEAVY_CHART, type ChartSize } from './vegaBudget'
import type { VisualTheme } from './theme'

// One request per worker; the page always terminates it on completion, error or timeout.
self.onmessage = (event: MessageEvent<{spec: Record<string, unknown>; theme: VisualTheme; size?: ChartSize}>) => {
  void drawChart(event.data.spec, event.data.theme, undefined, event.data.size).then(
    drawing => self.postMessage({drawing}),
    (error: unknown) => {
      const cause = error instanceof Error ? error.name : 'UnknownError'
      console.warn('Chart worker drawing failed', {cause})
      self.postMessage({error: error instanceof ChartRefused || cause === 'TextMetricsUnavailable' ? (error as Error).message : HEAVY_CHART})
    },
  )
}
