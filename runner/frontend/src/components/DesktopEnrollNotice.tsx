import { coreOrigin } from '../authClient'
import './DesktopEnrollNotice.css'

// Desktop enrollment lives in core: Settings → Account → Your desktops (#193). A session on a
// desktop that is not enrolled to its person, or whose credential was refused, says so in one
// line and links there. Runner only delivers the credential when core asks.
export default function DesktopEnrollNotice({ refused = false, reason = '' }: { refused?: boolean; reason?: string }) {
  const text = refused
    ? reason || 'The server refused this desktop’s credential.'
    : 'This desktop isn’t enrolled to you yet.'
  return <p className="desktop-notice" role="status" data-testid="desktop-enroll-notice">
    <span className="desktop-notice-text">{text}</span>
    <a className="desktop-notice-link" href={`${coreOrigin()}/settings#desktops`} target="_blank" rel="noopener noreferrer">Enroll in Settings</a>
  </p>
}
