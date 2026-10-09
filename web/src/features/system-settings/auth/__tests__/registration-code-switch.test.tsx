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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeEach, expect, it, vi } from 'vitest'

import type { RegistrationCodeSettings } from '@/features/registration-codes/api'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { BasicAuthSection } from '../basic-auth-section'

let settings: RegistrationCodeSettings
let testI18n: i18n

function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <SettingsPageProvider
          actionsContainer={null}
          suppressSectionHeader={false}
        >
          <BasicAuthSection
            defaultValues={{
              PasswordLoginEnabled: true,
              PasswordRegisterEnabled: true,
              EmailVerificationEnabled: true,
              RegisterEnabled: true,
              EmailDomainRestrictionEnabled: false,
              EmailAliasRestrictionEnabled: false,
              EmailDomainWhitelist: '',
            }}
          />
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
  return { ...view, client }
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
  vi.spyOn(api, 'get').mockImplementation(async (url, options) => ({
    data: {
      success: true,
      data:
        url === '/api/verify/methods'
          ? {
              scope: options?.params?.scope,
              methods: [{ method: 'password', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            }
          : { ...settings },
    },
  }))
  vi.spyOn(api, 'post').mockImplementation(async (url, body) => ({
    data: {
      success: true,
      data:
        url === '/api/verify'
          ? {
              scope: (body as { scope: string }).scope,
              method: 'password',
              proof_token: 'registration-proof',
              expires_at: Math.floor(Date.now() / 1000) + 300,
            }
          : {
              codes: ['REG-2345-6789-ABCD-EFGH', 'REG-JKLM-NPQR-STUV-WXYZ'],
              expires_at: 1800000000,
              expires_in: 3600,
            },
    },
  }))
  vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    settings = { ...settings, enabled: (body as { enabled: boolean }).enabled }
    return {
      data: {
        success: true,
        data: { enabled: (body as { enabled: boolean }).enabled },
      },
    }
  })
})

it('enables registration codes only after verifying the exact setting and refreshes the saved state', async () => {
  settings.enabled = false
  localStorage.setItem(
    'status',
    JSON.stringify({ registration_code_enabled: false })
  )
  const user = userEvent.setup()
  renderSettings()
  const toggle = await screen.findByRole('switch', {
    name: 'Registration codes enabled',
  })
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(api.put).not.toHaveBeenCalled()
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'test-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() => expect(toggle).toBeChecked())
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    {
      scope: 'registration_code.settings',
      context: { enabled: true },
      method: 'password',
      password: 'test-password',
    },
    expect.any(Object)
  )
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    '/api/option/registration-codes',
    { enabled: true },
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'registration-proof' },
      signal: expect.any(AbortSignal),
    })
  )
  expect(localStorage.getItem('status')).toBeNull()
  expect(
    screen.queryByRole('button', { name: 'Generate registration codes' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('heading', { name: 'Basic Authentication' })
  ).toBeVisible()
})

it('explains disabling, supports cancellation, and requires a separate proof before switching off', async () => {
  const user = userEvent.setup()
  renderSettings()
  const toggle = await screen.findByRole('switch', {
    name: 'Registration codes enabled',
  })
  await user.click(toggle)
  let confirmation = await screen.findByRole('alertdialog')
  expect(confirmation).toHaveTextContent(
    'New users will be able to register without a registration code.'
  )
  await user.click(within(confirmation).getByRole('button', { name: 'Cancel' }))
  expect(api.put).not.toHaveBeenCalled()
  await user.click(toggle)
  confirmation = await screen.findByRole('alertdialog')
  await user.click(
    within(confirmation).getByRole('button', { name: 'Turn off' })
  )
  expect(toggle).toBeChecked()
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'test-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() => expect(toggle).not.toBeChecked())
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'registration_code.settings',
      context: { enabled: false },
    }),
    expect.any(Object)
  )
  expect(
    screen.queryByRole('button', { name: 'Generate registration codes' })
  ).not.toBeInTheDocument()
})
