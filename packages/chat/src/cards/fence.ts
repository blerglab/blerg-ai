// The card fence: a ```card:<kind> block in assistant text whose body is a JSON object. The info
// string reaches the markdown renderer as the class of the code element (`language-card:<kind>`,
// the way react-markdown writes it); this reads the kind back and parses the body. Pure.

const CLASS_PREFIX = 'language-card:'

export interface CardFence {
  kind: string
  data: Record<string, unknown>
}

/** The kind named by a code element's class, or null when the fence is not a card fence. */
export function cardKind(className: string | undefined): string | null {
  if (!className || !className.startsWith(CLASS_PREFIX)) return null
  const kind = className.slice(CLASS_PREFIX.length)
  return kind ? kind : null
}

/** The kind and data of a card fence. Null when the class is not a card fence, when the body is
 *  not strict JSON (JSON.parse, no extension), or when it parses to anything but a plain object:
 *  the data is model output and the registry's component gets exactly the shape it was promised. */
export function parseCardFence(className: string | undefined, text: string): CardFence | null {
  const kind = cardKind(className)
  if (kind === null) return null
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch {
    return null
  }
  if (typeof data !== 'object' || data === null || Array.isArray(data)) return null
  return { kind, data: data as Record<string, unknown> }
}
