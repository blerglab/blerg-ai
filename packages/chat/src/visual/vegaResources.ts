import type { Loader } from 'vega'
export const denyVisualResources: Loader = {
  load: async () => { throw new Error('External chart resources are disabled') },
  sanitize: async () => { throw new Error('Chart links are disabled') },
  http: async () => { throw new Error('External chart resources are disabled') },
  file: async () => { throw new Error('External chart resources are disabled') },
}
