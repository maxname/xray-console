import { useQuery } from '@tanstack/react-query'

import { api } from '@/lib/api'

export const releaseKey = ['xray-release'] as const

/**
 * The newest Xray release and the servers behind it. The panel asks GitHub at
 * most once an hour, so asking the panel often costs nothing.
 */
export function useRelease() {
  return useQuery({
    queryKey: releaseKey,
    queryFn: () => api.getRelease(),
    staleTime: 60_000,
    // Follow an "update all" while it goes through the servers.
    refetchInterval: (q) => (q.state.data?.batch?.running ? 2000 : false),
  })
}
