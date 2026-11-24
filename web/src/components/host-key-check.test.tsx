import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import '@/i18n'
import { HostKeyCheck } from '@/components/host-key-check'
import { api, ApiError } from '@/lib/api'
import { useHostKeyCheck } from '@/lib/host-key'

function Form() {
  const [host, setHost] = useState('192.0.2.9')
  const check = useHostKeyCheck(host, '22')
  return (
    <>
      <input aria-label="host" value={host} onChange={(event) => setHost(event.target.value)} />
      <HostKeyCheck
        check={check}
        title="Отпечаток"
        prompt="Сначала получите отпечаток"
        failure={check.error === null ? null : { title: 'Не вышло' }}
      />
      <button disabled={!check.ready}>Добавить {check.hostKey}</button>
    </>
  )
}

function open() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Form />
    </QueryClientProvider>,
  )
}

const key = (name: string) => ({ host_key: `ssh-ed25519 ${name}`, fingerprint: `SHA256:${name}`, type: 'ssh-ed25519' })

describe('HostKeyCheck', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('lets the form go only after the key was seen and vouched for', async () => {
    const user = userEvent.setup()
    const probe = vi.spyOn(api, 'probeServer').mockResolvedValue(key('first'))
    open()

    expect(screen.getByText('Сначала получите отпечаток')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Добавить/ })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Получить отпечаток' }))
    expect(probe).toHaveBeenCalledWith('192.0.2.9', 22)
    expect(await screen.findByText('SHA256:first · ssh-ed25519')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Добавить/ })).toBeDisabled()

    await user.click(screen.getByRole('checkbox', { name: 'Отпечаток верный' }))
    expect(screen.getByRole('button', { name: 'Добавить ssh-ed25519 first' })).toBeEnabled()
  })

  it('forgets a key and its confirmation when the address changes', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'probeServer').mockResolvedValue(key('first'))
    open()
    await user.click(screen.getByRole('button', { name: 'Получить отпечаток' }))
    await user.click(await screen.findByRole('checkbox', { name: 'Отпечаток верный' }))

    await user.type(screen.getByLabelText('host'), '9')
    expect(screen.queryByText(/SHA256:first/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Добавить' })).toBeDisabled()

    // The key was fetched and vouched for at that address, and still is.
    await user.type(screen.getByLabelText('host'), '{Backspace}')
    expect(screen.getByRole('button', { name: 'Добавить ssh-ed25519 first' })).toBeEnabled()
  })

  it('asks for a confirmation again when the key is fetched again', async () => {
    const user = userEvent.setup()
    const probe = vi.spyOn(api, 'probeServer').mockResolvedValue(key('first'))
    open()
    await user.click(screen.getByRole('button', { name: 'Получить отпечаток' }))
    await user.click(await screen.findByRole('checkbox', { name: 'Отпечаток верный' }))

    probe.mockResolvedValue(key('second'))
    await user.click(screen.getByRole('button', { name: 'Получить отпечаток' }))
    expect(await screen.findByText('SHA256:second · ssh-ed25519')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Отпечаток верный' })).not.toBeChecked()
    expect(screen.getByRole('button', { name: /Добавить/ })).toBeDisabled()
  })

  it('says so when the address does not answer', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'probeServer').mockRejectedValue(new ApiError(502, 'ssh_unreachable', 'no route', {}))
    open()
    await user.click(screen.getByRole('button', { name: 'Получить отпечаток' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Не вышло')

    // The complaint was about that address, not the next one typed.
    await user.type(screen.getByLabelText('host'), '9')
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
