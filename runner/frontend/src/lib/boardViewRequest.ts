import { signedInAccount } from './liveBoardCard'
import { apiFetch } from '../apiFetch'

// Split panels share overlapping reads. Closing one panel cannot cancel another's request.
const pending = new Map<string, Promise<Response>>()
export async function boardViewRequest(url: string): Promise<Response> {
 const key = JSON.stringify([signedInAccount(), url])
 let request = pending.get(key)
 if (!request) {
  request = apiFetch(url).finally(() => pending.delete(key))
  pending.set(key, request)
 }
 return (await request).clone()
}
