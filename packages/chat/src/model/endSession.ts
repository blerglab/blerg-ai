// The words a confirmation says about ending a session, by where it runs (the session's runtime:
// "daemon" this machine, "docker" a local sandbox, "cluster" a pod) and whether it is an agent
// session (a conversation) or a terminal session.

export function endSessionText(runtime?: string | null, terminal = false): string {
  if (terminal) {
    switch (runtime) {
      case 'daemon':
        return 'The terminal session stops. Files are kept.'
      case 'docker':
        return 'The terminal session stops and its sandbox container is removed. Files and commits are kept.'
      case 'cluster':
        return 'The terminal session stops and its pod is removed. Work in the pod that was not pushed is lost.'
      default:
        return 'The terminal session stops. Work not yet saved may be lost.'
    }
  }
  switch (runtime) {
    case 'daemon':
      return 'The agent stops. The conversation and files are kept.'
    case 'docker':
      return 'The agent stops and its sandbox container is removed. The conversation, files and commits are kept.'
    case 'cluster':
      return 'The agent stops and its pod is removed. The conversation and published files are kept; work in the pod that was not pushed is lost.'
    default:
      // Not known where it runs (an older row, a start that has not said yet): nothing is promised.
      return 'The agent stops. Work not yet saved may be lost; the conversation is kept.'
  }
}
