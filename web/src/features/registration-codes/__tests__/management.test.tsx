/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { StrictMode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { toast } from 'sonner'
import { beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import type { RegistrationCodeRecord, RegistrationCodeSettings } from '../api'
import { RegistrationCodes } from '../index'

let settings: RegistrationCodeSettings
let records: RegistrationCodeRecord[]
let testI18n: i18n

function renderManagement(strict = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const page = (
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <RegistrationCodes />
      </QueryClientProvider>
    </I18nextProvider>
  )
  return render(strict ? <StrictMode>{page}</StrictMode> : page)
}

beforeEach(async () => {
  testI18n = createInstance()
  await testI18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
  })
  localStorage.clear()
  settings = {
    enabled: true,
    available: true,
    default_ttl_seconds: 600,
    max_count: 100,
    max_ttl_seconds: 86400,
  }
  records = []
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/registration-codes/settings'
          ? { ...settings }
          : { items: [...records], total: records.length },
    },
  }))
  vi.spyOn(api, 'post').mockImplementation(async () => {
    records = [
      {
        id: 1,
        name: 'Invitation',
        code: 'REG-2345-6789-ABCD-EFGH',
        status: 'unused',
        created_time: 1800000000,
        expired_time: 1800003600,
        used_time: 0,
        created_by: 1,
      },
    ]
    return {
      data: {
        success: true,
        data: {
          codes: [records[0].code],
          expires_at: 1800003600,
          expires_in: 3600,
        },
      },
    }
  })
})

it.each([
  { enabled: false, available: true },
  { enabled: true, available: false },
])(
  'disables generation when the service cannot issue codes: %j',
  async (state) => {
    settings = { ...settings, ...state }
    renderManagement()
    await screen.findByText('No registration codes')
    expect(
      screen.getByRole('button', { name: 'Generate registration codes' })
    ).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent(
      state.available
        ? 'Enable registration codes in Authentication → Basic Authentication.'
        : 'Registration code service is not configured. Contact your administrator.'
    )
    expect(api.post).not.toHaveBeenCalled()
  }
)

it('generates from the configured defaults without a password prompt and displays persistent copyable history', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  const view = renderManagement()
  await screen.findByText('No registration codes')
  await user.click(
    screen.getByRole('button', { name: 'Generate registration codes' })
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Generate registration codes',
  })
  expect(
    within(dialog).getByRole('spinbutton', { name: 'Validity (minutes)' })
  ).toHaveValue(10)
  expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Name' }),
    'Invitation'
  )
  fireEvent.change(
    within(dialog).getByRole('spinbutton', { name: 'Validity (minutes)' }),
    { target: { value: '60' } }
  )
  await user.click(within(dialog).getByRole('button', { name: /^Generate$/ }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(api.post).toHaveBeenCalledExactlyOnceWith(
    '/api/registration-codes/',
    { name: 'Invitation', count: 1, ttl_seconds: 3600 },
    expect.objectContaining({ signal: expect.any(AbortSignal) })
  )
  expect(vi.mocked(api.post).mock.calls[0][2]).not.toHaveProperty(
    'headers.X-Security-Proof'
  )
  expect(await screen.findByText('Invitation')).toBeVisible()
  expect(screen.getByText('Unused')).toBeVisible()
  expect(screen.queryByText(records[0].code)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Copy generated codes' }))
  expect(copy).toHaveBeenLastCalledWith(records[0].code)
  await user.click(
    screen.getByRole('button', { name: 'Copy registration code' })
  )
  expect(copy).toHaveBeenLastCalledWith(records[0].code)
  expect(localStorage.length).toBe(0)
  view.unmount()
  renderManagement()
  expect(await screen.findByText('Invitation')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Copy generated codes' })
  ).not.toBeInTheDocument()
})

it('rejects an invalid batch without sending a generation request', async () => {
  const user = userEvent.setup()
  renderManagement()
  await screen.findByText('No registration codes')
  await user.click(
    screen.getByRole('button', { name: 'Generate registration codes' })
  )
  const count = await screen.findByRole('spinbutton', {
    name: 'Number of registration codes',
  })
  fireEvent.change(count, { target: { value: '101' } })
  const form = count.closest('form')
  expect(form).not.toBeNull()
  if (form) fireEvent.submit(form)
  expect(
    await screen.findByText('Enter a whole number between 1 and 100.')
  ).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
})

it('closes a cancelled generation dialog without creating a code', async () => {
  const user = userEvent.setup()
  renderManagement()
  await screen.findByText('No registration codes')
  await user.click(
    screen.getByRole('button', { name: 'Generate registration codes' })
  )
  await user.click(
    within(await screen.findByRole('dialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(api.post).not.toHaveBeenCalled()
})

it('keeps the dialog open after a generation failure and reports the failure once', async () => {
  const error = vi.spyOn(toast, 'error')
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: false,
      message: 'Registration code service is unavailable',
    },
  })
  const user = userEvent.setup()
  renderManagement()
  await screen.findByText('No registration codes')
  await user.click(
    screen.getByRole('button', { name: 'Generate registration codes' })
  )
  await user.click(
    within(await screen.findByRole('dialog')).getByRole('button', {
      name: /^Generate$/,
    })
  )
  await waitFor(() =>
    expect(error).toHaveBeenCalledExactlyOnceWith(
      'Registration code service is unavailable'
    )
  )
  expect(screen.getByRole('dialog')).toBeVisible()
  expect(api.post).toHaveBeenCalledTimes(1)
})

it('shows used and expired states and queries history by name', async () => {
  records = [
    {
      id: 1,
      name: 'Used invitation',
      code: 'REG-2345-6789-ABCD-EFGH',
      status: 'used',
      created_time: 100,
      expired_time: 200,
      used_time: 150,
      created_by: 1,
    },
    {
      id: 2,
      name: 'Expired invitation',
      code: '',
      status: 'expired',
      created_time: 100,
      expired_time: 200,
      used_time: 0,
      created_by: 1,
    },
  ]
  const user = userEvent.setup()
  renderManagement()
  expect(await screen.findByText('Used')).toBeVisible()
  expect(screen.getByText('Expired')).toBeVisible()
  expect(screen.getByText('Unavailable')).toBeVisible()
  await user.type(
    screen.getByPlaceholderText('Filter by name...'),
    'Invitation'
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/registration-codes/',
      expect.objectContaining({
        params: { p: 1, page_size: 20, keyword: 'Invitation' },
      })
    )
  )
})

it('loads settings and history on the first StrictMode mount without a cancelled shared request', async () => {
  vi.mocked(api.get).mockRestore()
  const previousAdapter = api.defaults.adapter
  api.defaults.adapter = async (config) => ({
    data: {
      success: true,
      data:
        config.url === '/api/registration-codes/settings'
          ? settings
          : { items: [], total: 0 },
    },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  })
  try {
    renderManagement(true)
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Generate registration codes' })
      ).toBeEnabled()
    )
    expect(screen.getByText('No registration codes')).toBeVisible()
  } finally {
    api.defaults.adapter = previousAdapter
  }
})
