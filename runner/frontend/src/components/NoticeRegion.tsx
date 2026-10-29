import { useLayoutEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import './NoticeRegion.css'

// All floating notices share one bounded stack, including feature-local errors.
// Portaling leaves the composer and pane sizes unchanged.
const regions = new WeakMap<Document, { node: HTMLDivElement; release(): void }>()
function mountSlot(slot: HTMLDivElement) {
  const doc = slot.ownerDocument, win = doc.defaultView!
  let region = regions.get(doc)
  if (!region) {
    const node = doc.createElement('div')
    node.className = 'notice-region'
    node.setAttribute('role', 'region')
    node.setAttribute('aria-label', 'Notifications')
    doc.body.appendChild(node)
    let frame = 0
    let viewport: VisualViewport | null = null
    const watched = new Set<Element>()
    const resize = new ResizeObserver(schedule)
    function schedule() { if (!frame) frame = win.requestAnimationFrame(place) }
    function place() {
      frame = 0
      if (viewport !== win.visualViewport) {
        viewport?.removeEventListener('resize', schedule)
        viewport?.removeEventListener('scroll', schedule)
        viewport = win.visualViewport
        viewport?.addEventListener('resize', schedule)
        viewport?.addEventListener('scroll', schedule)
      }
      const top = (viewport?.offsetTop ?? 0) + 64
      const width = Math.min(340, (viewport?.width ?? win.innerWidth) - 24)
      const left = (viewport?.offsetLeft ?? 0) + (viewport?.width ?? win.innerWidth) - width - 12
      let bottom = (viewport?.offsetTop ?? 0) + (viewport?.height ?? win.innerHeight) - 8
      const protectedElements = new Set<Element>(doc.querySelectorAll('.agent-composer, .composer-attachments, [data-notice-protect]'))
      const active = doc.activeElement
      if (active?.matches('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"], [role="searchbox"]')) protectedElements.add(active)
      for (const element of watched) if (!protectedElements.has(element)) { resize.unobserve(element); watched.delete(element) }
      for (const element of protectedElements) {
        if (!watched.has(element)) { watched.add(element); resize.observe(element) }
        const box = element.getBoundingClientRect()
        if (box.width && box.height && box.right > left && box.left < left + width && box.bottom > top) bottom = Math.min(bottom, box.top - 8)
      }
      Object.assign(node.style, { top: 'auto', bottom: `${win.innerHeight - bottom}px`, left: `${left}px`, width: `${Math.max(0, width)}px`, maxHeight: `${Math.max(0, bottom - top)}px` })
    }
    const mutations = new MutationObserver(records => {
      if (records.some(record => !node.contains(record.target))) schedule()
    })
    mutations.observe(doc.body, { subtree: true, childList: true, attributes: true })
    doc.addEventListener('focusin', schedule)
    doc.addEventListener('focusout', schedule)
    win.addEventListener('resize', schedule)
    win.addEventListener('scroll', schedule, true)
    region = { node, release() {
      win.cancelAnimationFrame(frame)
      resize.disconnect(); mutations.disconnect()
      doc.removeEventListener('focusin', schedule); doc.removeEventListener('focusout', schedule)
      win.removeEventListener('resize', schedule); win.removeEventListener('scroll', schedule, true)
      viewport?.removeEventListener('resize', schedule); viewport?.removeEventListener('scroll', schedule)
      node.remove(); regions.delete(doc)
    } }
    regions.set(doc, region)
    place()
  }
  const current = region
  current.node.appendChild(slot)
  return () => { slot.remove(); if (!current.node.childElementCount) current.release() }
}

export default function NoticeRegion({ children }: { children: ReactNode }) {
  const [slot] = useState(() => { const node = document.createElement('div'); node.className = 'notice-slot'; return node })
  useLayoutEffect(() => mountSlot(slot), [slot])
  return createPortal(children, slot)
}
