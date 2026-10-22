// What a refused workspace-file read says. The UI branches on the STATUS (the route's contract);
// the server's text is only matched for the few distinctions the status cannot make, and is never
// shown itself.

/** The sentence for a read the server refused. */
export function workspaceErrorText(status: number, serverText = '', code?: string): string {
  const t = serverText.toLowerCase()
  // The server's machine-readable reason first; the sentence only for an older server.
  if (status === 409 && code) {
    if (code === 'busy') return 'The file reader is busy. Try again in a moment.'
    if (code === 'daemon_timeout' || code === 'changed' || code === 'invalid') return 'Couldn’t read the file just now. Try again.'
    if (code === 'not_running') return 'The session isn’t running, so its files can’t be read.'
  }
  switch (status) {
    case 401: return 'Sign in again to open files.'
    case 404: return t.startsWith('session not found') ? 'This session can’t be found.' : 'Not found in the session’s folder.'
    case 403: return t.startsWith('blerg doesn\'t open files that usually hold secrets') || t.startsWith('blerg doesn’t open files that usually hold secrets')
      ? 'Blerg doesn’t open files that usually hold secrets.'
      : 'Blerg can’t open that path.'
    case 409:
      if (t.includes('busy')) return 'The file reader is busy. Try again in a moment.'
      if (t.includes('did not provide') || t.includes('changed while') || t.includes('invalid')) return 'Couldn’t read the file just now. Try again.'
      return 'The session isn’t running, so its files can’t be read.'
    case 413: return 'Too large to open here.'
    default: return 'Couldn’t open that file.'
  }
}
