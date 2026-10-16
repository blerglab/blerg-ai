// A static preview of the start-steps chart on the Insights page for a person to look at in a browser:
// renders it with a rich report (one far outlier, so a log axis; then the same without it, linear)
// and writes the markup with the app's stylesheets to ~/insights-steps-preview.html.
// Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit StartSteps.preview
import { describe, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import StartSteps from './StartSteps'
import type { StepSpread } from '../lib/insights'

// The app's tsconfig has no Node types: the file system comes in untyped, through a dynamic import.
interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const row = (step: string, count: number, min: number, p25: number, p50: number, p75: number, p90: number, max: number, slow: number): StepSpread =>
  ({ runtime: 'cluster', step, count, min, p25, p50, p75, p90, max, slow })

const withOutlier: StepSpread[] = [
  row('queued', 37, 0, 0.1, 0.2, 0.4, 0.9, 2.1, 6),
  row('schedule', 37, 0.6, 0.9, 1.2, 1.8, 3.1, 7, 4),
  row('image', 37, 1.1, 1.6, 2.4, 4.5, 31, 1490, 7),
  row('clone', 37, 4, 7, 11, 16, 24, 48, 2),
  row('plugins', 37, 2, 3, 4, 6, 9, 14, 1),
  row('engine', 3, 5, 5.5, 6, 9, 11.4, 12, 0),
]
const withoutOutlier = withOutlier.map((s) => (s.step === 'image' ? { ...s, max: 95 } : s))

describe('StartSteps preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes ~/insights-steps-preview.html', async () => {
    render(<div className="insights"><section><h2>Pod startup: one far outlier (log axis), image row hovered</h2><StartSteps runtime="cluster" steps={withOutlier} /></section></div>)
    fireEvent.mouseEnter(screen.getAllByTestId('step-image')[0])
    render(<div className="insights"><section><h2>The same without the outlier (linear axis)</h2><StartSteps runtime="cluster" steps={withoutOutlier} /></section></div>)
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../index.css', './Insights.css'].map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    const markup = Array.from(document.body.children).map((el) => el.outerHTML).join('\n')
    writeFileSync(`${env.HOME}/insights-steps-preview.html`, `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><title>Insights start steps preview</title>
<style>${css}
body { margin: 0; min-height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
.insights section { margin-bottom: 140px; }
</style>
<script>/* ?light previews the light theme */ if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'</script>
</head><body>${markup}</body></html>
`)
  })
})
