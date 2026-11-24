import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'

import { Segmented } from '@/components/segmented'

const OPTIONS = [
  { value: 'day', label: 'Сутки' },
  { value: 'week', label: 'Неделя' },
  { value: 'month', label: 'Месяц' },
] as const

function Harness() {
  const [value, setValue] = useState<(typeof OPTIONS)[number]['value']>('week')
  return <Segmented label="Период" options={OPTIONS} value={value} onChange={setValue} />
}

function chosen() {
  return screen.getByRole('radio', { checked: true })
}

describe('Segmented', () => {
  it('is one group of radio buttons with one of them chosen', () => {
    render(<Harness />)
    expect(screen.getByRole('radiogroup', { name: 'Период' })).toBeInTheDocument()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
    expect(chosen()).toHaveTextContent('Неделя')
  })

  it('is one stop for the Tab key, and arrows move the choice', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    await user.tab()
    expect(chosen()).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(chosen()).toHaveTextContent('Месяц')
    expect(chosen()).toHaveFocus()
    // Round the end, and back.
    await user.keyboard('{ArrowRight}')
    expect(chosen()).toHaveTextContent('Сутки')
    await user.keyboard('{ArrowLeft}')
    expect(chosen()).toHaveTextContent('Месяц')
    await user.keyboard('{Home}')
    expect(chosen()).toHaveTextContent('Сутки')
    // Only the chosen one is in the tab order.
    await user.tab()
    expect(document.body).toHaveFocus()
  })

  it('takes a click', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('radio', { name: 'Сутки' }))
    expect(chosen()).toHaveTextContent('Сутки')
  })
})
