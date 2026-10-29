import type { PermissionMeaning } from '../lib/capabilities'
import './PermissionChip.css'

export default function PermissionChip({ permission, testId }: { permission: PermissionMeaning; testId?: string }) {
  return <span className={`permission-chip${permission.risky ? ' risky' : ''}`} title={permission.description}
    aria-description={permission.description} data-testid={testId}>{permission.label}</span>
}
