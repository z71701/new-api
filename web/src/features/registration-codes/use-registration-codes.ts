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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useSecureVerification } from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'

import {
  generateRegistrationCodes,
  getRegistrationCodeSettings,
  updateRegistrationCodeSettings,
  type GenerateRegistrationCodesRequest,
  type RegistrationCodeBatch,
} from './api'

const queryKey = ['registration-code-settings']

export function useRegistrationCodes() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const operation = useRef<AbortController | null>(null)
  const [batch, setBatch] = useState<RegistrationCodeBatch | null>(null)
  const settings = useQuery({
    queryKey,
    queryFn: ({ signal }) => getRegistrationCodeSettings(signal),
  })

  useEffect(
    () => () => {
      operation.current?.abort()
      cancelVerification()
    },
    [cancelVerification]
  )

  const update = useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: async (enabled: boolean) => {
      const controller = new AbortController()
      operation.current = controller
      const proof = await verification.requestVerification({
        scope: 'registration_code.settings',
        context: { enabled },
        title: t('Verify registration code settings'),
        description: enabled
          ? t('New users will need a valid registration code.')
          : t(
              'New users will be able to register without a registration code.'
            ),
      })
      if (!proof || controller.signal.aborted) return
      await updateRegistrationCodeSettings(
        enabled,
        proof.proof_token,
        controller.signal
      )
      if (controller.signal.aborted) return
      setBatch(null)
      await Promise.all([
        client.invalidateQueries({ queryKey }),
        client.invalidateQueries({ queryKey: ['system-options'] }),
        client.invalidateQueries({ queryKey: ['status'] }),
      ])
      try {
        window.localStorage.removeItem('status')
      } catch {
        /* Storage may be disabled. */
      }
      toast.success(t('Setting updated successfully'))
    },
    onError: (error) => handleServerError(error),
  })

  const generate = useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: async (request: GenerateRegistrationCodesRequest) => {
      const controller = new AbortController()
      operation.current = controller
      const result = await generateRegistrationCodes(request, controller.signal)
      if (!controller.signal.aborted) {
        setBatch(result)
        await client.invalidateQueries({ queryKey: ['registration-codes'] })
      }
    },
    onError: (error) => handleServerError(error),
  })

  const busy = update.isPending || generate.isPending
  return { settings, update, generate, batch, busy, verification }
}
