import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import '@/i18n'
import { api, type Device, type OperationFeed, type RuleSet } from '@/lib/api'
import { DeviceDialog } from '@/pages/device-dialogs'

const sets = [{ id: 1, name: 'Основной', entry: { id: 1, name: 'ams-1' } }] as unknown as RuleSet[]

function device(change: Partial<Device>): Device {
  return {
    id: 7,
    name: 'Роутер',
    host: '192.0.2.9',
    ssh_port: 22,
    ssh_user: 'root',
    rule_set_id: 1,
    rule_set_name: 'Основной',
    entry: null,
    socks_port: 10808,
    http_port: 10809,
    lan: false,
    proxy_auth: false,
    auto_push: false,
    status: 'failed',
    step: 'entry',
    os_name: 'Debian GNU/Linux 12',
    arch: 'x86_64',
    xray_version: 'v26.1.2',
    operation_id: 5,
    failure: { code: 'entry_pending', step: 'entry', message: 'waiting', details: { server: 'ams-1' } },
    online: false,
    service: '',
    seen_at: null,
    pending: false,
    traffic: { up: 0, down: 0 },
    ...change,
  } as Device
}

function feed(kind: string, steps: [string, string][]): OperationFeed {
  return {
    operation: {
      id: 5,
      server_id: 0,
      kind,
      status: 'failed',
      steps: steps.map(([id, status]) => ({ id, status, detail: '' })),
      error: null,
      params: {},
      result: {},
      started_at: 0,
      finished_at: 1,
    },
    log: [{ seq: 1, at: 0, step: 'connect', kind: 'cmd', text: 'ssh -i panel root@192.0.2.9' }],
  } as unknown as OperationFeed
}

function open(dev: Device, job: OperationFeed) {
  vi.spyOn(api, 'getOperation').mockResolvedValue(job)
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <DeviceDialog device={dev} sets={sets} onClose={() => {}} />
    </QueryClientProvider>,
  )
}

const SETUP: [string, string][] = [
  ['connect', 'done'],
  ['system', 'done'],
  ['xray', 'done'],
  ['entry', 'failed'],
  ['config', 'pending'],
  ['check', 'pending'],
]

describe('DeviceDialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('offers to go on from the step a set-up stopped at, or to undo it', async () => {
    const user = userEvent.setup()
    const retry = vi.spyOn(api, 'retryDevice').mockResolvedValue({ devices: [], local: null } as never)
    open(device({}), feed('device_setup', SETUP))

    // The job's own record, not a copy in the device's row.
    await user.click(await screen.findByText('Шаги и журнал последней задачи'))
    expect(screen.getAllByRole('listitem')).toHaveLength(6)
    expect(screen.getByText(/ssh -i panel root@192\.0\.2\.9/)).toBeInTheDocument()

    expect(screen.getByRole('button', { name: 'Запустить установку заново' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Откатить установку' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Повторить с шага «Входной сервер»' }))
    expect(retry).toHaveBeenCalledWith(7, '')
  })

  it('asks before undoing a set-up', async () => {
    const user = userEvent.setup()
    const rollback = vi
      .spyOn(api, 'rollbackDevice')
      .mockResolvedValue({ devices: [], local: null } as never)
    open(device({}), feed('device_setup', SETUP))

    await user.click(await screen.findByRole('button', { name: 'Откатить установку' }))
    expect(rollback).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Да, откатить' }))
    expect(rollback).toHaveBeenCalledWith(7)
  })

  it('does not offer to undo a push: the previous settings are back already', async () => {
    open(
      device({
        status: 'ready',
        step: 'config',
        failure: { code: 'port_in_use', step: 'config', message: 'taken', details: { restored: true } },
      }),
      feed('device_push', [
        ['connect', 'done'],
        ['entry', 'done'],
        ['config', 'failed'],
        ['check', 'pending'],
      ]),
    )
    expect(
      await screen.findByRole('button', { name: 'Повторить с шага «Настройки и запуск клиента»' }),
    ).toBeInTheDocument()
    // The job is there to be read before deciding what is offered.
    await screen.findByText('Шаги и журнал последней задачи')
    expect(screen.queryByRole('button', { name: 'Откатить установку' })).toBeNull()
    // A device that works is not set up again from scratch from here.
    expect(screen.queryByRole('button', { name: 'Запустить установку заново' })).toBeNull()
  })

  it('offers only a set-up for a device nothing was done to', async () => {
    open(device({ status: 'new', step: '', failure: null, operation_id: null }), feed('device_setup', SETUP))
    expect(await screen.findByRole('button', { name: 'Запустить установку' })).toBeInTheDocument()
    expect(screen.queryByText('Шаги и журнал последней задачи')).toBeNull()
    expect(screen.queryByRole('button', { name: /Повторить с шага/ })).toBeNull()
  })
})
