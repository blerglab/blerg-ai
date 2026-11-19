/** Separate the server's legacy escalation envelope for display only. Never rewrite message data. */
export function questionPresentation(body: string, worker?: string) {
  // quotedName replaces embedded quotes with apostrophes and leaves backslashes literal.
  const match = /^(?:Child|Worker) "([^"\n]*)", (?:parent|via) "([^"\n]*)": /.exec(body)
  if (match) return { text: body.slice(match[0].length), asker: match[1], parent: match[2] }
  return { text: body, asker: worker || 'Main', parent: undefined }
}
