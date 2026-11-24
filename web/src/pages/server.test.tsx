import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import '@/i18n'
import type { Server } from '@/lib/api'
import { DeleteDialog } from '@/pages/server-delete'

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}))

const server = { id: 3, name: 'ams-1', status: 'ready', manifest: [] } as unknown as Server

function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <QueryClientProvider client={new QueryClient()}>
      <button onClick={() => setOpen(true)}>open</button>
      <DeleteDialog server={server} open={open} onOpenChange={setOpen} />
    </QueryClientProvider>
  )
}

describe('DeleteDialog', () => {
  it('forgets the typed name when it is closed', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    await user.click(screen.getByText('open'))
    const submit = () => screen.getByRole('button', { name: 'Удалить сервер' })
    expect(submit()).toBeDisabled()
    await user.type(screen.getByRole('textbox'), 'ams-1')
    expect(submit()).toBeEnabled()

    // Cancelled, then opened again: the confirmation has to be given again,
    // not found waiting with the button already armed.
    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    await user.click(screen.getByText('open'))
    expect(screen.getByRole('textbox')).toHaveValue('')
    expect(submit()).toBeDisabled()
  })
})
