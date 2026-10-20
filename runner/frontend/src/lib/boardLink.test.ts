import { describe, expect, it } from 'vitest'
import { boardCardUrl } from './boardLink'

const at = (protocol: string, hostname: string, port = '') => ({ protocol, hostname, origin: `${protocol}//${hostname}${port ? `:${port}` : ''}` })

describe('boardCardUrl', () => {
  it('opens the card on board.<domain> beside runner.<domain>', () => {
    expect(boardCardUrl('b 1', 7, at('https:', 'runner.example.com'))).toBe('https://board.example.com/boards/b%201/cards/7')
  })
  it('uses the Board port on a desktop install', () => {
    expect(boardCardUrl('b1', 3, at('http:', 'localhost', '8083'))).toBe('http://localhost:8082/boards/b1/cards/3')
  })
})
