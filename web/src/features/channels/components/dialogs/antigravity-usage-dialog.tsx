/*
Copyright (C) 2023-2026 QuantumNous and the new-api contributors.

This program is free software: you can redistribute it under the terms
of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import { handleServerError } from '@/lib/handle-server-error'
import {
  createServerError,
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import {
  getAntigravityUsage,
  refreshAntigravityCredential,
  type AntigravityModelQuota,
} from '../../api'
import { channelsQueryKeys } from '../../lib'
import type { Channel } from '../../types'

type AntigravityUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channel: Channel | null
}

function formatIsoTime(
  value: string | undefined,
  locale: string | undefined
): string | null {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  return date.toLocaleString(locale)
}

function clampPercent(value: number | undefined): number {
  if (typeof value !== 'number' || Number.isNaN(value)) return 0
  return Math.max(0, Math.min(100, value))
}

function ModelQuotaCard(props: {
  item: AntigravityModelQuota
  locale: string | undefined
}) {
  const { t } = useTranslation()
  const { item, locale } = props
  const used = clampPercent(item.used_percent)
  const remaining = clampPercent(item.remaining_percent)
  const resetText = formatIsoTime(item.reset_time, locale)

  return (
    <div className='space-y-2 rounded-lg border p-3.5'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0'>
          <div className='truncate text-sm font-medium'>
            {item.display_name || item.model}
          </div>
          {item.display_name && item.display_name !== item.model && (
            <div className='text-muted-foreground truncate font-mono text-xs'>
              {item.model}
            </div>
          )}
        </div>
        {typeof item.remaining_percent === 'number' ? (
          <span className='font-mono text-xs font-semibold tabular-nums'>
            {remaining.toFixed(1)}% {t('remaining')}
          </span>
        ) : (
          <span className='text-muted-foreground text-xs'>-</span>
        )}
      </div>
      {typeof item.used_percent === 'number' && (
        <Progress value={used} className='h-2' />
      )}
      {resetText && (
        <p className='text-muted-foreground text-xs'>
          {t('Resets at')}: {resetText}
        </p>
      )}
    </div>
  )
}

export function AntigravityUsageDialog({
  open,
  onOpenChange,
  channel,
}: AntigravityUsageDialogProps) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const [isRefreshingToken, setIsRefreshingToken] = useState(false)
  const channelId = channel?.id ?? 0
  const intlLocale = toIntlLocale(i18n.language)

  const usageQuery = useQuery({
    queryKey: ['channel-antigravity-usage', channelId],
    queryFn: async () =>
      requireServerSuccess(await getAntigravityUsage(channelId)),
    enabled: open && channelId > 0,
    refetchOnWindowFocus: false,
    retry: false,
    meta: { errorToast: false },
  })

  const data = usageQuery.data?.data
  const usage = data?.usage
  const credential = data?.credential

  const handleRefreshCredential = async () => {
    if (!channelId) return
    setIsRefreshingToken(true)
    try {
      const res = await refreshAntigravityCredential(channelId)
      if (!res.success) {
        throw createServerError(res, t('Failed to refresh credential'))
      }
      toast.success(t('Credential refreshed'))
      void usageQuery.refetch()
      void queryClient.invalidateQueries({
        queryKey: channelsQueryKeys.detail(channelId),
      })
    } catch (error) {
      handleServerError(error, t('Refresh failed'))
    } finally {
      setIsRefreshingToken(false)
    }
  }

  const tierLabel =
    usage?.paid_tier_name ||
    usage?.paid_tier ||
    usage?.current_tier_name ||
    usage?.current_tier ||
    credential?.plan_tier

  const expiresText = formatIsoTime(credential?.expires_at, intlLocale)

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      contentClassName='max-h-[85vh] sm:max-w-2xl'
      title={t('Antigravity Subscription Usage')}
      description={
        channel ? `${channel.name || `#${channel.id}`} (#${channel.id})` : ''
      }
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={handleRefreshCredential}
            disabled={isRefreshingToken || usageQuery.isFetching}
          >
            {isRefreshingToken ? (
              <Loader2 className='mr-1.5 h-3.5 w-3.5 animate-spin' />
            ) : (
              <RefreshCw className='mr-1.5 h-3.5 w-3.5' />
            )}
            {t('Refresh credential')}
          </Button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => {
              void usageQuery.refetch()
            }}
            disabled={usageQuery.isFetching}
          >
            <RefreshCw
              className={`mr-1.5 h-3.5 w-3.5 ${usageQuery.isFetching ? 'animate-spin' : ''}`}
            />
            {t('Refresh')}
          </Button>
        </>
      }
    >
      {usageQuery.isLoading ? (
        <div className='space-y-3 py-2'>
          <Skeleton className='h-20 w-full rounded-lg' />
          <Skeleton className='h-28 w-full rounded-lg' />
          <Skeleton className='h-28 w-full rounded-lg' />
        </div>
      ) : usageQuery.isError ? (
        <ErrorState
          className='min-h-0 p-4'
          title={t('Failed to fetch Antigravity subscription usage')}
          description={getServerErrorMessage(
            usageQuery.error,
            t('Failed to fetch Antigravity subscription usage')
          )}
          onRetry={() => {
            void usageQuery.refetch()
          }}
        />
      ) : usage ? (
        <div className='space-y-4 py-1'>
          <div className='flex flex-wrap items-center gap-2 rounded-lg border p-3.5 text-xs'>
            {tierLabel && (
              <StatusBadge
                label={tierLabel}
                variant='info'
                size='sm'
                copyable={false}
                showDot={false}
              />
            )}
            {(usage.project_id || credential?.project_id) && (
              <span className='text-muted-foreground font-mono'>
                {t('Project')}: {usage.project_id || credential?.project_id}
              </span>
            )}
            {(usage.email || credential?.email) && (
              <span className='text-muted-foreground'>
                {usage.email || credential?.email}
              </span>
            )}
            {expiresText && (
              <span className='text-muted-foreground'>
                {t('Token expires')}: {expiresText}
              </span>
            )}
          </div>

          {usage.credits && usage.credits.length > 0 && (
            <div className='space-y-2 rounded-lg border p-4'>
              <div className='text-sm font-medium'>
                {t('Available Credits')}
              </div>
              <div className='grid gap-2 sm:grid-cols-2'>
                {usage.credits.map((c, idx) => (
                  <div
                    key={`${c.credit_type}-${idx}`}
                    className='bg-muted/30 flex items-center justify-between rounded-md px-3 py-2 text-xs'
                  >
                    <span className='text-muted-foreground'>
                      {c.credit_type}
                    </span>
                    <span className='font-mono font-semibold tabular-nums'>
                      {c.credit_amount}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {usage.models && usage.models.length > 0 ? (
            <div className='space-y-2'>
              <div className='text-sm font-medium'>{t('Model Quotas')}</div>
              <div className='grid gap-2.5 sm:grid-cols-2'>
                {usage.models.map((item) => (
                  <ModelQuotaCard
                    key={item.model}
                    item={item}
                    locale={intlLocale}
                  />
                ))}
              </div>
            </div>
          ) : (
            <div className='text-muted-foreground rounded-lg border p-4 text-xs'>
              {t('No model quota details returned by upstream.')}
            </div>
          )}
        </div>
      ) : null}
    </Dialog>
  )
}
