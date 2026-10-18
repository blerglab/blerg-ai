import type { CardOriginInfo } from './api';
import { SafeAnchor } from './CardLinks';

export function RestrictedMarker({ sensitive }: { sensitive?: boolean }) {
  return sensitive ? <span className="chip" title="Reproduction details are visible only to board admins">Restricted details</span> : null;
}

export function CardOrigin({ origin }: { origin?: CardOriginInfo | null }) {
  if (!origin) return null;
  return <section aria-label="Ticket origin" style={{ marginTop: 12, fontSize: 13 }}>
    <h4>Filed from a session</h4>
    <SafeAnchor href={origin.session_url}>Originating session</SafeAnchor>
    <dl className="settings-grid">
      {origin.repo && <><dt>Repository</dt><dd>{origin.repo}</dd></>}
      {origin.commit && <><dt>Commit</dt><dd className="mono">{origin.commit}</dd></>}
      {origin.engine && <><dt>Engine</dt><dd>{origin.engine}</dd></>}
      {origin.component_version && <><dt>Component version</dt><dd>{origin.component_version}</dd></>}
      {origin.component_commit && <><dt>Component commit</dt><dd className="mono">{origin.component_commit}</dd></>}
    </dl>
  </section>;
}
