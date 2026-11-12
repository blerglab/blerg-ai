import { useMemo } from 'react'
import type { TransportFiles, WorkspaceGitRef } from '../transport/types'
import WorkspaceFileViewer from './WorkspaceFileViewer'

/** A repository-qualified, immutable file revision, safe to retain in a tab layout. */
export interface CommitFileVersion {
  sha: string
  side: 'original' | 'changed'
  sessionId?: string
  member?: WorkspaceGitRef
}

export default function CommitFileViewer({ version, path, sessionId, files, onClose, embedded = true }: {
  version: CommitFileVersion; path: string; sessionId: string; files: TransportFiles; onClose(): void; embedded?: boolean
}) {
  const owner = version.sessionId ?? sessionId
  const { sha, side, member } = version
  const commitFiles = useMemo<TransportFiles>(() => ({ ...files,
    workspaceFile: (_session, requested, signal) => requested !== path
      ? Promise.reject(new Error('Relative commit files are unavailable'))
      : files.commitFile ? files.commitFile(owner, sha, path, side, signal, member)
        : Promise.reject(new Error('Commit files are unavailable')),
  }), [files, owner, sha, path, side, member])
  return <WorkspaceFileViewer embedded={embedded} commitBlob sessionId={owner} path={path} files={commitFiles} onClose={onClose} />
}
