import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import '@/i18n'
import { api, type Server, type ServerCert, type ServerConfig } from '@/lib/api'
import { MaskPanel } from '@/pages/server-mask'

const day = 86400
const now = Math.floor(Date.now() / 1000)

function cert(change: Partial<ServerCert>): ServerCert {
  return {
    domain: 'cdn.example.net',
    not_before: now - 30 * day,
    not_after: now + 60 * day + 3600,
    issuer: 'R13',
    checked_at: now,
    error: '',
    overdue: false,
    ...change,
  }
}

function show(config: Partial<ServerConfig>, serverCert: ServerCert | null) {
  const onUpdate = vi.fn().mockResolvedValue(null)
  const server = { id: 3, name: 'ams-1', host: '203.0.113.14', cert: serverCert } as Server
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MaskPanel
        server={server}
        config={{ mask_host: 'www.example.com', domain: '', running_domain: '', ...config } as ServerConfig}
        disabled={false}
        onUpdate={onUpdate}
      />
    </QueryClientProvider>,
  )
  return onUpdate
}

const ownDomain = { mask_host: 'cdn.example.net', domain: 'cdn.example.net', running_domain: 'cdn.example.net' }

describe('the masking panel of a server', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('says when the certificate of a domain the server runs is good until', () => {
    show(ownDomain, cert({}))
    expect(screen.getByText('Свой домен')).toBeInTheDocument()
    expect(screen.getByText(/Сертификат действует до .* осталось 60 дней/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Продлить сейчас' })).toBeEnabled()
  })

  it('does not call a certificate that is running late fine', () => {
    show(ownDomain, cert({ not_after: now + 9 * day + 3600, overdue: true }))
    expect(screen.getByText(/осталось 9 дней/)).toBeInTheDocument()
    expect(screen.getByText(/должен был уже продлить его сам/)).toBeInTheDocument()
  })

  it('says a certificate has run out, and what the last look found wrong', () => {
    show(ownDomain, cert({ not_after: now - 2 * day, overdue: true, error: 'x509: certificate has expired' }))
    expect(screen.getByText(/Сертификат истёк/)).toBeInTheDocument()
    expect(screen.getByText(/x509: certificate has expired/)).toBeInTheDocument()
  })

  it('offers no renewal for a domain that is not on the server yet', () => {
    show({ ...ownDomain, running_domain: '' }, null)
    expect(screen.getByText(/Домен ещё не применён/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Продлить сейчас' })).not.toBeInTheDocument()
    // Nor does it show the certificate of whatever the server ran before.
    expect(screen.queryByText(/Сертификат действует/)).not.toBeInTheDocument()
  })

  it('says what applying will take away when the domain is given up', () => {
    show({ running_domain: 'cdn.example.net' }, cert({}))
    expect(screen.getByText('Чужой сайт')).toBeInTheDocument()
    expect(screen.getByText(/перестанет отвечать за cdn.example.net/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Продлить сейчас' })).not.toBeInTheDocument()
  })

  it('saves a domain as an edit and asks DNS about it on the way', async () => {
    const check = vi.spyOn(api, 'checkDomain').mockResolvedValue({
      domain: 'cdn.example.net',
      addresses: ['198.51.100.200'],
      expected: ['203.0.113.14'],
      match: false,
      other: ['198.51.100.200'],
    })
    const user = userEvent.setup()
    const onUpdate = show({}, null)

    await user.click(screen.getByRole('button', { name: 'Изменить домен и маскировку' }))
    await user.click(screen.getByRole('radio', { name: /Свой домен/ }))
    await user.type(screen.getByLabelText('Доменное имя'), ' CDN.example.net ')
    // The record to make at the registrar, for this server's address.
    expect(screen.getByText(/^cdn\.example\.net\s+A\s+203\.0\.113\.14$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Проверить запись' }))
    expect(check).toHaveBeenCalledWith('CDN.example.net', '203.0.113.14')
    expect(
      await screen.findByText(/Домен указывает на 198.51.100.200, а сервер — 203.0.113.14/),
    ).toBeInTheDocument()

    // The check is a courtesy: the edit is still the administrator's to make.
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(onUpdate).toHaveBeenCalledWith({ domain: 'CDN.example.net' })
  })
})
