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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { useRegistrationCodes } from '@/features/registration-codes/use-registration-codes'

import { SettingsSwitchField } from '../components/settings-form-layout'

export function RegistrationCodeSwitch() {
  const { t } = useTranslation()
  const codes = useRegistrationCodes()
  const [confirmDisable, setConfirmDisable] = useState(false)
  const settings = codes.settings.data

  if (codes.settings.isError) {
    return <ErrorState onRetry={() => void codes.settings.refetch()} />
  }

  return (
    <div data-settings-form-span='full'>
      <SettingsSwitchField
        controlId='registration-code-enabled'
        checked={settings?.enabled ?? false}
        onCheckedChange={(enabled) => {
          if (codes.busy || !settings) return
          if (enabled) codes.update.mutate(true)
          else setConfirmDisable(true)
        }}
        disabled={
          codes.busy || !settings || (!settings.available && !settings.enabled)
        }
        label={t('Registration codes enabled')}
        description={t(
          'When enabled, new users must enter a valid one-time registration code.'
        )}
      />
      {settings && !settings.available && (
        <Alert>
          <AlertDescription>
            {t(
              'Registration code service is not configured. Contact your administrator.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        title={t('Turn off registration codes?')}
        desc={t(
          'New users will be able to register without a registration code.'
        )}
        confirmText={t('Turn off')}
        handleConfirm={() => {
          setConfirmDisable(false)
          codes.update.mutate(false)
        }}
      />
      <SecureVerificationDialog {...codes.verification.dialogProps} />
    </div>
  )
}
