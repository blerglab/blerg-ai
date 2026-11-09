import type { KeyboardEvent } from 'react'

export function explorerTreeKey(event: KeyboardEvent<HTMLUListElement>) {
  const item = (event.target as HTMLElement).closest<HTMLButtonElement>('[role="treeitem"]')
  if (!item) return
  const items = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="treeitem"]')]
  const index = items.indexOf(item)
  let next: HTMLButtonElement | undefined
  if (event.key === 'ArrowDown') next = items[Math.min(items.length - 1, index + 1)]
  else if (event.key === 'ArrowUp') next = items[Math.max(0, index - 1)]
  else if (event.key === 'Home') next = items[0]
  else if (event.key === 'End') next = items.at(-1)
  else if (event.key === 'ArrowRight') {
    if (item.getAttribute('aria-expanded') === 'false') (item.querySelector<HTMLElement>('.workspace-chevron') ?? item).click()
    else if (item.getAttribute('aria-expanded') === 'true') next = item.parentElement?.querySelector<HTMLButtonElement>(':scope > [role="group"] > li > [role="treeitem"]') ?? undefined
  } else if (event.key === 'ArrowLeft') {
    if (item.getAttribute('aria-expanded') === 'true') (item.querySelector<HTMLElement>('.workspace-chevron') ?? item).click()
    else next = item.parentElement?.parentElement?.closest('li')?.querySelector<HTMLButtonElement>('[role="treeitem"]') ?? undefined
  } else return
  event.preventDefault()
  if (next) {
    items.forEach(row => { row.tabIndex = row === next ? 0 : -1 })
    next.focus()
  }
}
