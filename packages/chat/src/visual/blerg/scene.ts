/** Commit a sanitized geometry tree to a retained surface. Preparation never mutates the
 * visible scene; the synchronous commit updates viewport and descendants in one transaction.
 * Sibling keys identify axes, ticks and legend entries across density changes. */
export function commitScene(current: Element, next: Element): void {
  for (const attr of [...current.attributes]) if (!next.hasAttribute(attr.name)) current.removeAttribute(attr.name)
  for (const attr of [...next.attributes]) if (current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value)
  if (!next.children.length) {
    if (current.textContent !== next.textContent) current.textContent = next.textContent
    return
  }
  const old = [...current.children]
  const keyed = new Map(old.filter(child => child.hasAttribute('data-scene-key')).map(child => [child.getAttribute('data-scene-key'), child]))
  const used = new Set<Element>()
  for (const [index, child] of [...next.children].entries()) {
    const key = child.getAttribute('data-scene-key')
    const candidate = key ? keyed.get(key) : old[index]
    const retained = candidate && !used.has(candidate) && candidate.tagName === child.tagName && candidate.getAttribute('data-scene-key') === key ? candidate : child.cloneNode(true) as Element
    if (retained !== child) commitScene(retained, child)
    used.add(retained)
    if (current.children[index] !== retained) current.insertBefore(retained, current.children[index] ?? null)
  }
  for (const child of [...current.children]) if (!used.has(child)) child.remove()
}
