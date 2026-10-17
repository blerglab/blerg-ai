import type { ArtifactInfo } from './artifacts'
import { isViewable } from './artifactViews'

export interface ArtifactLink {
  artifact: ArtifactInfo
  /** Absolute runner URL, also correct when the chat is embedded on another origin. */
  href: string
}

/** Match only a known viewable file on the transport's exact published session URL.
 * The URL is supplied by the transport, never inferred from model-authored link text. */
export function resolveArtifactLink(raw: string | undefined, sessionUrl: string | null | undefined, artifacts: readonly ArtifactInfo[]): ArtifactLink | null {
  if (!raw || !sessionUrl) return null
  try {
    const base = new URL(sessionUrl)
    if (!['https:', 'http:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) return null
    const link = new URL(raw, base)
    if (link.origin !== base.origin || link.pathname !== base.pathname || link.username || link.password || link.hash) return null
    // Do not capture navigation that also requests a different action, or an ambiguous ID.
    const params = [...link.searchParams]
    if (params.length !== 1 || params[0][0] !== 'artifact') return null
    const artifact = artifacts.find(a => a.id === params[0][1] && isViewable(a.view))
    return artifact ? { artifact, href: link.href } : null
  } catch {
    return null
  }
}
