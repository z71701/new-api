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
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

export interface RegistrationCodeSettings {
  enabled: boolean
  available: boolean
  default_ttl_seconds: number
  max_count: number
  max_ttl_seconds: number
}

export interface RegistrationCodeBatch {
  codes: string[]
  expires_at: number
  expires_in: number
}

export interface GenerateRegistrationCodesRequest {
  name: string
  count: number
  ttl_seconds: number
}

export interface RegistrationCodeRecord {
  id: number
  name: string
  code: string
  status: 'unused' | 'used' | 'expired'
  created_time: number
  expired_time: number
  used_time: number
  created_by: number
}

export function getRegistrationCodes(
  page: number,
  size: number,
  keyword: string,
  signal: AbortSignal
) {
  return authResult<{ items: RegistrationCodeRecord[]; total: number }>(
    api.get('/api/registration-codes/', {
      ...authRequestOptions,
      disableDuplicate: true,
      params: { p: page, page_size: size, keyword },
      signal,
    })
  )
}

export function getRegistrationCodeSettings(signal?: AbortSignal) {
  return authResult<RegistrationCodeSettings>(
    api.get('/api/registration-codes/settings', {
      ...authRequestOptions,
      disableDuplicate: true,
      signal,
    })
  )
}

export function updateRegistrationCodeSettings(
  enabled: boolean,
  proof: string,
  signal: AbortSignal
) {
  return authResult<{ enabled: boolean }>(
    api.put(
      '/api/option/registration-codes',
      { enabled },
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proof },
        signal,
      }
    )
  )
}

export function generateRegistrationCodes(
  request: GenerateRegistrationCodesRequest,
  signal: AbortSignal
) {
  return authResult<RegistrationCodeBatch>(
    api.post('/api/registration-codes/', request, {
      ...authRequestOptions,
      signal,
    })
  )
}
