import type { ToolSchemaRefresh } from '../types'
import { toolSchemaGuidanceText } from '../lib/toolSchemaGuidance'

// Adapter-reported behavior, never inferred from an engine's display name.
// className: the caller's styling (the Capabilities ⓘ popover); without one, the inline style it has always had.
export default function ToolSchemaGuidance({ policy, className }: { policy?: ToolSchemaRefresh; className?: string }) {
  const text = toolSchemaGuidanceText(policy)
  if (className) return <p data-testid="tool-schema-guidance" className={className}>{text}</p>
  return <p data-testid="tool-schema-guidance" style={{ color: 'var(--mist)', fontSize: 12, lineHeight: 1.5, margin: 0 }}>{text}</p>
}
