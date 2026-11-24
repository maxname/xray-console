import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import '@/i18n'
import { api, ApiError } from '@/lib/api'
import { LoginPage } from '@/pages/login'
import { ThemeProvider } from '@/theme/theme-provider'

const navigate = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
}))

function renderLogin() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  render(
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <LoginPage />
      </QueryClientProvider>
    </ThemeProvider>,
  )
}

async function submit(password: string) {
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Пароль'), password)
  await user.click(screen.getByRole('button', { name: 'Войти' }))
  return user
}

describe('LoginPage', () => {
  beforeEach(() => {
    navigate.mockReset()
    vi.restoreAllMocks()
  })

  it('says how many attempts are left, in grammatical Russian', async () => {
    vi.spyOn(api, 'login').mockRejectedValue(
      new ApiError(401, 'invalid_credentials', 'wrong', {
        attempts_left: 2,
        lock_minutes: 15,
        client_ip: '203.0.113.80',
      }),
    )
    renderLogin()
    await submit('wrong password')

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Неверное имя или пароль' +
        'Осталось 2 попытки, затем вход с этого адреса блокируется на 15 минут.',
    )
    expect(screen.getByText(/Адрес 203\.0\.113\.80/)).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('uses the singular for the last attempt', async () => {
    vi.spyOn(api, 'login').mockRejectedValue(
      new ApiError(401, 'invalid_credentials', 'wrong', {
        attempts_left: 1,
        lock_minutes: 15,
        client_ip: '203.0.113.80',
      }),
    )
    renderLogin()
    await submit('wrong password')

    expect(await screen.findByRole('alert')).toHaveTextContent('Осталась 1 попытка')
  })

  it('reports a lock with the wait rounded up to whole minutes', async () => {
    vi.spyOn(api, 'login').mockRejectedValue(
      new ApiError(429, 'locked', 'locked', {
        retry_after_seconds: 121,
        client_ip: '203.0.113.80',
      }),
    )
    renderLogin()
    await submit('wrong password')

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Повторите через 3 минуты.',
    )
  })

  it('asks for the code only once the server wants it, then sends it', async () => {
    const login = vi
      .spyOn(api, 'login')
      .mockRejectedValueOnce(new ApiError(401, 'totp_required', 'code'))
      .mockResolvedValueOnce({ ok: true })
    renderLogin()

    expect(screen.queryByLabelText('Код из приложения')).not.toBeInTheDocument()
    const user = await submit('correct horse battery')

    // A prompt, not a failure: no error is shown for the first answer.
    const code = await screen.findByLabelText('Код из приложения')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    await user.type(code, '123456')
    await user.click(screen.getByRole('button', { name: 'Войти' }))

    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }))
    expect(login).toHaveBeenLastCalledWith({
      username: 'admin',
      password: 'correct horse battery',
      code: '123456',
      remember: false,
    })
  })
})
