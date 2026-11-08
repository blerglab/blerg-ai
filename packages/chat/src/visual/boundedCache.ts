// Hash-indexed, collision-checked LRU. Keys never retain unbounded source strings;
// entry accounting includes the source and the owned value, in UTF-16/decoded bytes.
export function sourceHash(text: string): string {
  let a = 2166136261, b = 3339675911
  for (let i = 0; i < text.length; i++) { a = Math.imul(a ^ text.charCodeAt(i), 16777619); b = Math.imul(b ^ text.charCodeAt(i), 2246822519) }
  return `${(a >>> 0).toString(16)}:${(b >>> 0).toString(16)}:${text.length}`
}
export class BoundedCache<T> {
  private entries = new Map<string, {source: string; value: T; bytes: number}>()
  private bytes = 0
  private budget: number
  private count: number
  constructor(budget: number, count = 8) { this.budget = budget; this.count = count }
  get(source: string): T | undefined {
    const key = sourceHash(source), entry = this.entries.get(key)
    if (!entry || entry.source !== source) return
    this.entries.delete(key); this.entries.set(key, entry)
    return entry.value
  }
  set(source: string, value: T, bytes: number): void {
    const key = sourceHash(source), previous = this.entries.get(key)
    if (previous) { this.bytes -= previous.bytes; this.entries.delete(key) }
    bytes += source.length * 2
    if (bytes > this.budget) return
    this.entries.set(key, {source, value, bytes}); this.bytes += bytes
    while (this.bytes > this.budget || this.entries.size > this.count) {
      const first = this.entries.keys().next().value!
      this.bytes -= this.entries.get(first)!.bytes; this.entries.delete(first)
    }
  }
  clear(): void { this.entries.clear(); this.bytes = 0 }
}
