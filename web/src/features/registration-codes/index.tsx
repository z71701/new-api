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
import { useQuery } from '@tanstack/react-query'
import type { ColumnDef, PaginationState } from '@tanstack/react-table'
import { Plus, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { DataTablePage, useDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { MaskedValueDisplay } from '@/components/masked-value-display'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'

import { getRegistrationCodes, type RegistrationCodeRecord } from './api'
import { RegistrationCodesGenerateDialog } from './generate-dialog'
import { useRegistrationCodes } from './use-registration-codes'

export function RegistrationCodes() {
  const { t, i18n } = useTranslation()
  const codes = useRegistrationCodes()
  const [open, setOpen] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 20,
  })
  const history = useQuery({
    queryKey: ['registration-codes', pagination, keyword],
    queryFn: ({ signal }) =>
      getRegistrationCodes(
        pagination.pageIndex + 1,
        pagination.pageSize,
        keyword,
        signal
      ),
    gcTime: 0,
    staleTime: 0,
    refetchInterval: 30000,
  })
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const columns = useMemo<ColumnDef<RegistrationCodeRecord>[]>(() => {
    const labels = {
      unused: t('Unused'),
      used: t('Used'),
      expired: t('Expired'),
    }
    return [
      {
        accessorKey: 'id',
        header: t('ID'),
        size: 80,
        meta: { mobileHidden: true },
        cell: ({ row }) => <TableId value={row.original.id} />,
      },
      {
        accessorKey: 'name',
        header: t('Name'),
        size: 160,
        meta: { mobileTitle: true },
        cell: ({ row }) => row.original.name || t('Registration code'),
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        size: 110,
        meta: { mobileBadge: true },
        cell: ({ row }) => (
          <StatusBadge
            copyable={false}
            label={labels[row.original.status]}
            variant={row.original.status === 'unused' ? 'success' : 'neutral'}
          />
        ),
      },
      {
        accessorKey: 'code',
        header: t('Registration code'),
        size: 240,
        cell: ({ row }) =>
          row.original.code ? (
            <MaskedValueDisplay
              label={t('Registration code')}
              fullValue={row.original.code}
              maskedValue={`${row.original.code.slice(0, 8)}-****-****-****`}
              copyTooltip={t('Copy registration code')}
              copyAriaLabel={t('Copy registration code')}
            />
          ) : (
            <span>{t('Unavailable')}</span>
          ),
      },
      {
        accessorKey: 'created_time',
        header: t('Created Time'),
        size: 180,
        cell: ({ row }) =>
          new Date(row.original.created_time * 1000).toLocaleString(locale),
      },
      {
        accessorKey: 'expired_time',
        header: t('Expiration Time'),
        size: 180,
        cell: ({ row }) =>
          new Date(row.original.expired_time * 1000).toLocaleString(locale),
      },
    ]
  }, [t, locale])
  const { table } = useDataTable({
    data: history.data?.items ?? [],
    columns,
    getRowId: (row) => String(row.id),
    enableRowSelection: false,
    enableSorting: false,
    manualPagination: true,
    manualFiltering: true,
    columnFilters: [],
    totalCount: history.data?.total ?? 0,
    pagination,
    onPaginationChange: setPagination,
    globalFilter: keyword,
    onGlobalFilterChange: (value) => {
      setKeyword(value)
      setPagination((previous) => ({ ...previous, pageIndex: 0 }))
    },
  })
  const canGenerate =
    codes.settings.data?.enabled && codes.settings.data.available

  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>
          {t('Registration codes')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          {codes.batch && (
            <CopyButton
              value={codes.batch.codes.join('\n')}
              size='sm'
              variant='outline'
              tooltip={t('Copy generated codes')}
            >
              {t('Copy generated codes')}
            </CopyButton>
          )}
          <Button
            size='sm'
            variant='outline'
            onClick={() => void history.refetch()}
            disabled={history.isFetching}
          >
            <RefreshCw className='size-4' />
            {t('Refresh')}
          </Button>
          <Button
            size='sm'
            onClick={() => setOpen(true)}
            disabled={!canGenerate || codes.busy}
          >
            <Plus className='size-4' />
            {t('Generate registration codes')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-3'>
            {codes.settings.isError ? (
              <ErrorState onRetry={() => void codes.settings.refetch()} />
            ) : (
              codes.settings.data &&
              !canGenerate && (
                <Alert>
                  <AlertDescription>
                    {t(
                      codes.settings.data.available
                        ? 'Enable registration codes in Authentication → Basic Authentication.'
                        : 'Registration code service is not configured. Contact your administrator.'
                    )}
                  </AlertDescription>
                </Alert>
              )
            )}
            {history.isError ? (
              <ErrorState onRetry={() => void history.refetch()} />
            ) : (
              <DataTablePage
                table={table}
                columns={columns}
                isLoading={history.isPending}
                isFetching={history.isFetching}
                emptyTitle={t('No registration codes')}
                emptyDescription={t(
                  'Generate registration codes to invite new users.'
                )}
                toolbarProps={{
                  searchPlaceholder: t('Filter by name...'),
                  searchDebounceMs: 300,
                }}
              />
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {open && codes.settings.data && (
        <RegistrationCodesGenerateDialog
          open
          onOpenChange={setOpen}
          settings={codes.settings.data}
          generate={codes.generate}
        />
      )}
    </>
  )
}
