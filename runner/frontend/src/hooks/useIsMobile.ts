import { useMediaQuery } from './useMediaQuery'

const QUERY = '(max-width: 899px)'

export function useIsMobile(): boolean {
  return useMediaQuery(QUERY)
}
