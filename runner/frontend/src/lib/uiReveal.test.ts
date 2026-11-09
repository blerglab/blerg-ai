import { afterEach, expect, it } from 'vitest'
import { canFollowReveal, validReveal } from './uiReveal'
afterEach(() => { document.body.innerHTML = '' })
it('accepts only bounded, relative paths and known actions', () => {
 expect(validReveal({action:'open',target:'src/a.ts'})).toBe(true)
 for (const target of ['../a','/etc/a','a/../b','a\\b','a\nfile','']) expect(validReveal({action:'open',target})).toBe(false)
 expect(validReveal({action:'panel',target:'shell'})).toBe(false)
})
it('defers follow while editing or in any modal', () => {
 expect(canFollowReveal()).toBe(true)
 document.body.innerHTML = '<textarea></textarea>'
 document.querySelector('textarea')!.focus()
 expect(canFollowReveal()).toBe(false)
 document.body.innerHTML = '<div role="dialog"></div>'
 expect(canFollowReveal()).toBe(false)
})

it('allows focus on the Follow checkbox but rejects bidi paths', () => {
 document.body.innerHTML = '<input type="checkbox" data-reveal-follow="">'
 document.querySelector('input')!.focus()
 expect(canFollowReveal()).toBe(true)
 for (const target of ['a\u202eb', 'a\u2066b', 'a\u200fb']) expect(validReveal({action:'open',target})).toBe(false)
})

it('permits an untouched automatic composer focus but defers a draft', () => {
 document.body.innerHTML='<textarea data-autofocused="true"></textarea>'
 const box=document.querySelector('textarea')!;box.focus()
 expect(canFollowReveal()).toBe(true)
 box.value='draft';expect(canFollowReveal()).toBe(false)
 box.value='';delete box.dataset.autofocused;expect(canFollowReveal()).toBe(false)
})

it('still defers while another form checkbox owns focus', () => {
 document.body.innerHTML='<input type="checkbox" aria-label="Approve a change">'
 document.querySelector('input')!.focus()
 expect(canFollowReveal()).toBe(false)
})
