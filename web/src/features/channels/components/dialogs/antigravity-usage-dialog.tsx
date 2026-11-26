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
import { Info, Loader2, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatPercent } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import {
  createServerError,
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import {
  getAntigravityUsage,
  refreshAntigravityCredential,
  type AntigravityQuotaBucket,
  type AntigravityQuotaGroup,
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

// quotaGroupLabel names an allowance by its stable key, falling back to the
// upstream display name for groups the gateway does not classify.
function quotaGroupLabel(
  group: AntigravityQuotaGroup,
  t: (key: string) => string
): string {
  switch (group.group_key) {
    case 'antigravity-gemini-models':
      return t('Gemini models')
    case 'antigravity-claude-and-gpt-models':
      return t('Claude and GPT models')
    case 'antigravity-gemini-4-argon':
      return t('Gemini 4 Argon')
    default:
      return group.display_name || t('Quota windows')
  }
}

function quotaWindowLabel(
  window: string | undefined,
  fallback: string | undefined,
  t: (key: string) => string
): string {
  switch ((window ?? '').toLowerCase()) {
    case 'weekly':
    case 'week':
      return t('Weekly window')
    case '5h':
    case 'five-hour':
    case 'five_hour':
      return t('5-hour window')
    default:
      return fallback || t('Window')
  }
}

const percentTextClassName: Record<'danger' | 'warning' | 'info', string> = {
  danger: 'text-destructive',
  warning: 'text-warning',
  info: 'text-info',
}

// quotaPercentVariant colours a window by how much of it is already consumed.
function quotaPercentVariant(
  usedPercent: number
): 'danger' | 'warning' | 'info' {
  if (usedPercent >= 95) {
    return 'danger'
  }
  if (usedPercent >= 80) {
    return 'warning'
  }
  return 'info'
}

// quotaGroupHint explains an allowance in one short line. The upstream sends a
// long English paragraph (plan marketing for Argon, a model list for the shared
// pools), so the dialog states the rule itself and keeps the raw text out of the
// layout.
function quotaGroupHint(
  group: AntigravityQuotaGroup,
  t: (key: string) => string
): string {
  switch (group.group_key) {
    case 'antigravity-gemini-models':
      return t(
        'Gemini 4 Hydrogen and the Flash models share this 5-hour and weekly allowance.'
      )
    case 'antigravity-claude-and-gpt-models':
      return t('Claude and GPT models share this 5-hour and weekly allowance.')
    case 'antigravity-gemini-4-argon':
      return t(
        'A separate weekly allowance for Gemini 4 Argon. Ultra plans include a larger Argon share inside the Gemini pool.'
      )
    default:
      return group.description ?? ''
  }
}

// QuotaWindowCard shows one window (a 5-hour or weekly bucket) of an allowance,
// laid out like the Coding Plan cards: the remaining share on top, the bar below
// and the reset time last.
function QuotaWindowCard(props: {
  bucket: AntigravityQuotaBucket
  locale: string | undefined
}) {
  const { t } = useTranslation()
  const bucket = props.bucket
  const fraction = bucket.remaining_fraction
  const hasFraction = typeof fraction === 'number'
  const remaining = hasFraction ? clampPercent(fraction * 100) : 0
  const used = hasFraction ? 100 - remaining : 0
  const resetText = formatIsoTime(bucket.reset_time, props.locale)
  const windowLabel = quotaWindowLabel(bucket.window, bucket.display_name, t)

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-2'>
          <CardTitle className='text-sm font-semibold'>{windowLabel}</CardTitle>
          {bucket.disabled && (
            <StatusBadge
              label={t('Disabled')}
              variant='neutral'
              size='sm'
              copyable={false}
              showDot={false}
            />
          )}
        </div>
      </CardHeader>
      <CardContent className='flex flex-col gap-2 p-3 pt-0'>
        <div className='flex items-end justify-between gap-3'>
          <span className='flex items-baseline gap-1.5'>
            <span
              className={cn(
                'text-2xl leading-none font-semibold tabular-nums',
                hasFraction
                  ? percentTextClassName[quotaPercentVariant(used)]
                  : 'text-muted-foreground'
              )}
            >
              {hasFraction ? formatPercent(remaining) : '-'}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t('remaining')}
            </span>
          </span>
          <span className='text-muted-foreground text-xs tabular-nums'>
            {t('Used')}: {hasFraction ? formatPercent(used) : '-'}
          </span>
        </div>
        {hasFraction ? (
          <Progress
            value={used}
            aria-label={`${windowLabel}: ${formatPercent(used)}`}
          />
        ) : (
          <div className='bg-muted h-2 rounded-full' />
        )}
        <p className='text-muted-foreground text-xs'>
          {t('Reset time:')} {resetText ?? '-'}
        </p>
      </CardContent>
    </Card>
  )
}

// QuotaGroupSection shows one allowance: its name, an info icon carrying the
// explanation, and a card per reported window. Argon is never folded into Gemini
// because it drains on its own schedule.
function QuotaGroupSection(props: {
  group: AntigravityQuotaGroup
  locale: string | undefined
}) {
  const { t } = useTranslation()
  const group = props.group
  const buckets = group.buckets ?? []
  const hint = quotaGroupHint(group, t)

  return (
    <div className='space-y-2'>
      <div className='flex items-center gap-1.5'>
        <span className='text-sm font-medium'>{quotaGroupLabel(group, t)}</span>
        {hint && (
          <TooltipProvider delay={100}>
            <Tooltip>
              <TooltipTrigger
                aria-label={t('What this allowance covers')}
                className='text-muted-foreground/60 hover:text-muted-foreground focus-visible:ring-ring shrink-0 rounded-full transition-colors focus-visible:ring-2 focus-visible:outline-none'
              >
                <Info className='size-3.5' aria-hidden='true' />
              </TooltipTrigger>
              <TooltipContent side='top' className='max-w-xs leading-relaxed'>
                {hint}
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </div>
      {buckets.length > 0 ? (
        <div className='grid gap-2.5 sm:grid-cols-2'>
          {buckets.map((bucket, index) => (
            <QuotaWindowCard
              key={bucket.bucket_id || `${bucket.window}-${index}`}
              bucket={bucket}
              locale={props.locale}
            />
          ))}
        </div>
      ) : (
        <div className='text-muted-foreground rounded-lg border p-3 text-xs'>
          {t('No quota window reported by upstream.')}
        </div>
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
      {usageQuery.isLoading && (
        <div className='space-y-3 py-2'>
          <Skeleton className='h-20 w-full rounded-lg' />
          <Skeleton className='h-28 w-full rounded-lg' />
          <Skeleton className='h-28 w-full rounded-lg' />
        </div>
      )}
      {usageQuery.isError && (
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
      )}
      {!usageQuery.isError && usage && (
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
                {usage.credits.map((c) => (
                  <div
                    key={`${c.credit_type}-${c.credit_amount}`}
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

          {usage.quota_groups && usage.quota_groups.length > 0 ? (
            <div className='space-y-4'>
              <div className='text-sm font-medium'>{t('Quota windows')}</div>
              {usage.quota_groups.map((group) => (
                <QuotaGroupSection
                  key={group.group_key || group.display_name || 'quota-group'}
                  group={group}
                  locale={intlLocale}
                />
              ))}
            </div>
          ) : (
            <div className='text-muted-foreground rounded-lg border p-4 text-xs'>
              {t('No quota window reported by upstream.')}
            </div>
          )}
        </div>
      )}
    </Dialog>
  )
}
