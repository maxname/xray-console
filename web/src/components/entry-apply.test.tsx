import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import '@/i18n'
import { EntryApply } from '@/components/entry-apply'
import type { ChainServer } from '@/lib/api'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}))

function entry(change: Partial<ChainServer>): ChainServer {
  return {
    id: 1,
    name: 'ams-1',
    location: 'Амстердам',
    state: 'up',
    tcp_ms: 12,
    pending: true,
    changed: ['policies'],
    busy: false,
    restart: false,
    ...change,
  }
}

function show(server: ChainServer) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <EntryApply entry={server} text="Новая подписка ещё не на сервере." onRefresh={async () => {}} />
    </QueryClientProvider>,
  )
}

describe('EntryApply', () => {
  it('says that clients and rules reach Xray without a restart', () => {
    show(entry({}))
    expect(screen.getByText(/Xray не перезапускается/)).toBeInTheDocument()
    // And what that means for a client that was taken off.
    expect(screen.getByText(/закончатся сами/)).toBeInTheDocument()
    expect(screen.queryByText(/будет перезапущен/)).not.toBeInTheDocument()
  })

  it('says so when something else waiting on the server makes it a restart', () => {
    show(entry({ changed: ['policies', 'xhttp.mode'], restart: true }))
    expect(screen.getByText(/будет перезапущен/)).toBeInTheDocument()
    expect(screen.queryByText(/не перезапускается/)).not.toBeInTheDocument()
  })
})
