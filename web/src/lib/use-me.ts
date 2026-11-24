import { useQuery } from '@tanstack/react-query'

import { api } from '@/lib/api'

/** The signed-in administrator and the panel's own details, cached app-wide. */
export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: () => api.me(),
    staleTime: 5 * 60 * 1000,
    retry: false,
  })
}
