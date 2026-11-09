export interface UIReveal { action: 'open' | 'reveal' | 'attachment' | 'panel'; target: string }
export function validReveal(value: unknown): value is UIReveal {
 if (!value || typeof value !== 'object') return false
 const { action, target } = value as UIReveal
 if (typeof target !== 'string' || !target || target.length > 2048 || /[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u.test(target) || [...target].some(character => character.charCodeAt(0) < 32 || (character.charCodeAt(0) >= 127 && character.charCodeAt(0) <= 159))) return false
 if (action === 'panel') return ['live','plan','history','files','capabilities'].includes(target)
 if (action === 'attachment') return /^[a-f0-9]{32}$/.test(target)
 return ['open','reveal'].includes(action) && !target.startsWith('/') && !/[\\:]/.test(target) && target.split('/').every(part => part !== '..' && part !== '.' && part !== '')
}
export function canFollowReveal(): boolean {
 const editing = document.activeElement?.closest('input:not([data-reveal-follow]), textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]')
 const idleAutoFocus = editing instanceof HTMLTextAreaElement && editing.dataset.autofocused === "true" && editing.value === ""
 return (!editing || idleAutoFocus) && !document.querySelector('[role="dialog"]:not([hidden]), [aria-modal="true"]:not([hidden]), dialog[open]') && document.visibilityState !== 'hidden'
}
export const UI_REVEAL = 'blerg-ui-reveal'
export const UI_REVEAL_APPLY = 'blerg-ui-reveal-apply'
