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
import { zodResolver } from '@hookform/resolvers/zod'
import type { UseMutationResult } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type {
  GenerateRegistrationCodesRequest,
  RegistrationCodeSettings,
} from './api'

interface RegistrationCodesGenerateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  settings: RegistrationCodeSettings
  generate: UseMutationResult<void, Error, GenerateRegistrationCodesRequest>
}

export function RegistrationCodesGenerateDialog(
  props: RegistrationCodesGenerateDialogProps
) {
  const { t } = useTranslation()
  const maxMinutes = Math.floor(props.settings.max_ttl_seconds / 60)
  const countMessage = t('Enter a whole number between 1 and {{max}}.', {
    max: props.settings.max_count,
  })
  const minutesMessage = t('Enter a whole number between 1 and {{max}}.', {
    max: maxMinutes,
  })
  const schema = z.object({
    name: z.string().trim().max(64, t('Name must not exceed 64 characters.')),
    count: z
      .number({ error: countMessage })
      .int(countMessage)
      .min(1, countMessage)
      .max(props.settings.max_count, countMessage),
    minutes: z
      .number({ error: minutesMessage })
      .int(minutesMessage)
      .min(1, minutesMessage)
      .max(maxMinutes, minutesMessage),
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: {
      name: '',
      count: 1,
      minutes: Math.max(1, Math.ceil(props.settings.default_ttl_seconds / 60)),
    },
  })
  return (
    <Dialog
      open={props.open}
      onOpenChange={(open) => {
        if (!props.generate.isPending) props.onOpenChange(open)
      }}
      title={t('Generate registration codes')}
      description={t('Each registration code can be used once.')}
      contentClassName='sm:max-w-md'
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={props.generate.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='generate-registration-codes'
            disabled={props.generate.isPending}
          >
            {props.generate.isPending ? t('Generating...') : t('Generate')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='generate-registration-codes'
          className='space-y-4'
          onSubmit={form.handleSubmit((data) =>
            props.generate.mutate(
              {
                name: data.name,
                count: data.count,
                ttl_seconds: data.minutes * 60,
              },
              { onSuccess: () => props.onOpenChange(false) }
            )
          )}
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    maxLength={64}
                    disabled={props.generate.isPending}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='count'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Number of registration codes')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    min={1}
                    max={props.settings.max_count}
                    disabled={props.generate.isPending}
                    onChange={(event) =>
                      field.onChange(event.target.valueAsNumber)
                    }
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='minutes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Validity (minutes)')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    min={1}
                    max={maxMinutes}
                    disabled={props.generate.isPending}
                    onChange={(event) =>
                      field.onChange(event.target.valueAsNumber)
                    }
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
