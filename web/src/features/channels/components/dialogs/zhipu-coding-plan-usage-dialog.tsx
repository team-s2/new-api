/*
Copyright (C) 2023-2026 QuantumNous

This program is free software under the GNU Affero General Public License
as published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version. See the GNU Affero General
Public License for more details at <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { RefreshCw, Zap } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { formatTimestampToDate } from '@/lib/format'

import {
  useZhipuCodingPlanReset,
  type ZhipuCodingPlanUsageLimit,
  type ZhipuCodingPlanUsageResponse,
  type ZhipuCodingPlanResetCard,
} from '../../api'

type ZhipuCodingPlanUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName: string
  channelId?: number
  response: ZhipuCodingPlanUsageResponse | null
  onRefresh: () => void | Promise<void>
  isRefreshing: boolean
}

const mcpNames: Record<string, string> = {
  'search-prime': 'Web search MCP',
  'web-reader': 'Web reader MCP',
  zread: 'Open-source repository MCP',
  vision: 'Vision MCP',
}

function clampPercentage(value: number | undefined): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.min(100, value ?? 0))
}

function formatResetTime(value: number | undefined): string {
  if (!value || !Number.isFinite(value)) return '-'
  return formatTimestampToDate(Math.floor(value / 1000))
}

function UsageCard(props: {
  title: string
  description: string
  limit?: ZhipuCodingPlanUsageLimit
  showDetails?: boolean
}) {
  const { t } = useTranslation()
  // Absent limits mean the plan generation has no such window: credit-based
  // plans (2026-07-30 onward) deduct MCP calls from the shared credit pool
  // instead of granting a separate monthly MCP quota.
  if (!props.limit) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{props.title}</CardTitle>
          <CardDescription>{props.description}</CardDescription>
        </CardHeader>
        <CardContent>
          <p className='text-muted-foreground text-sm'>
            {t('Not included in this plan')}
          </p>
        </CardContent>
      </Card>
    )
  }
  const percentage = clampPercentage(props.limit.percentage)
  let usageText = `${percentage}%`
  if (
    typeof props.limit.current_value === 'number' &&
    typeof props.limit.usage === 'number'
  ) {
    usageText = `${props.limit.current_value.toLocaleString()} / ${props.limit.usage.toLocaleString()}`
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{props.title}</CardTitle>
        <CardDescription>{props.description}</CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-3'>
        <div className='flex items-end justify-between gap-4'>
          <span className='flex items-baseline gap-1.5'>
            <span className='text-2xl font-semibold'>{usageText}</span>
            {props.limit.unit && (
              <span className='text-muted-foreground text-sm'>
                {t(props.limit.unit)}
              </span>
            )}
          </span>
          <span className='text-muted-foreground text-sm'>{percentage}%</span>
        </div>
        <Progress value={percentage} />
        <p className='text-muted-foreground text-xs'>
          {t('Reset time:')} {formatResetTime(props.limit?.next_reset_time)}
        </p>
        {props.showDetails && (props.limit?.usage_details?.length ?? 0) > 0 && (
          <div className='flex flex-col gap-1 text-sm'>
            {props.limit?.usage_details?.map((detail) => (
              <div
                key={detail.model_code}
                className='flex justify-between gap-4'
              >
                <span className='text-muted-foreground'>
                  {t(mcpNames[detail.model_code] || detail.model_code)}
                </span>
                <span>{detail.usage.toLocaleString()}</span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function earliestExpiry(cards: ZhipuCodingPlanResetCard[] | undefined) {
  const stamps = (cards ?? [])
    .map((card) => card.expire_at)
    .filter((value): value is number => typeof value === 'number' && value > 0)
  if (stamps.length === 0) return undefined
  return Math.min(...stamps)
}

function ResetCardRow(props: {
  title: string
  cards: ZhipuCodingPlanResetCard[] | undefined
  lastUsedAt?: number | null
  resetType: 'FIVE_HOUR' | 'WEEK'
  onUse: (resetType: 'FIVE_HOUR' | 'WEEK') => void
  pending: boolean
  disabled: boolean
}) {
  const { t } = useTranslation()
  const count = props.cards?.length ?? 0
  const expires = earliestExpiry(props.cards)
  return (
    <div className='flex flex-wrap items-center justify-between gap-3'>
      <div className='flex min-w-0 flex-col gap-0.5'>
        <span className='text-sm font-medium'>{props.title}</span>
        <span className='text-muted-foreground text-xs'>
          {count > 0
            ? t('{{available}} available · earliest expires {{time}}', {
                available: count,
                time: formatResetTime(expires),
              })
            : t('No reset cards available')}
          {props.lastUsedAt
            ? ` · ${t('Last used')} ${formatResetTime(props.lastUsedAt)}`
            : ''}
        </span>
      </div>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || props.pending || count === 0}
        onClick={() => props.onUse(props.resetType)}
      >
        <Zap data-icon='inline-start' />
        {t('Apply reset')}
      </Button>
    </div>
  )
}

export function ZhipuCodingPlanUsageDialog(
  props: ZhipuCodingPlanUsageDialogProps
) {
  const { t } = useTranslation()
  const data = props.response?.data
  const [usingReset, setUsingReset] = useState<'FIVE_HOUR' | 'WEEK' | null>(
    null
  )

  const handleUseReset = async (resetType: 'FIVE_HOUR' | 'WEEK') => {
    if (!props.channelId || usingReset) return
    setUsingReset(resetType)
    try {
      const res = await useZhipuCodingPlanReset(props.channelId, resetType)
      if (!res.success) {
        throw new Error(res.message || t('Failed to use reset card'))
      }
      toast.success(t('Reset card applied'))
      await props.onRefresh()
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t('Failed to use reset card')
      )
    } finally {
      setUsingReset(null)
    }
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Zhipu Coding Plan Account Info')}
      description={`${t('Channel:')} ${props.channelName}`}
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <div className='flex gap-2'>
          <Button
            variant='outline'
            onClick={props.onRefresh}
            disabled={props.isRefreshing}
          >
            <RefreshCw
              data-icon='inline-start'
              className={props.isRefreshing ? 'animate-spin' : undefined}
            />
            {props.isRefreshing ? t('Refreshing...') : t('Refresh')}
          </Button>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Close')}
          </Button>
        </div>
      }
    >
      {!data ? (
        <div className='grid gap-4 md:grid-cols-3'>
          {[0, 1, 2].map((item) => (
            <Skeleton key={item} className='h-48' />
          ))}
        </div>
      ) : (
        <>
          {data.level && (
            <p className='text-muted-foreground text-sm'>
              {t('Plan level:')} {data.level.toUpperCase()}
            </p>
          )}
          <div className='grid gap-4 md:grid-cols-3'>
            <UsageCard
              title={t('5-hour usage limit')}
              description={t('Shared model usage in the current 5-hour window')}
              limit={data.five_hour}
            />
            <UsageCard
              title={t('Weekly usage limit')}
              description={t('Shared model usage in the current weekly window')}
              limit={data.weekly}
            />
            <UsageCard
              title={t('Monthly MCP limit')}
              description={t('Monthly shared usage for Coding Plan MCP tools')}
              limit={data.mcp_monthly}
              showDetails
            />
          </div>
          <Card>
            <CardHeader>
              <CardTitle>{t('Reset cards')}</CardTitle>
              <CardDescription>
                {t(
                  'Consume a card to reset the matching quota window immediately'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent className='flex flex-col gap-4'>
              {!data.reset && (
                <Alert variant='destructive'>
                  <Zap />
                  <AlertTitle>{t('Reset card query failed')}</AlertTitle>
                  <AlertDescription>
                    {data.reset_unavailable_reason
                      ? `${t(
                          'Re-run OAuth login for this channel to enable reset cards.'
                        )} (${data.reset_unavailable_reason})`
                      : t('Re-run OAuth login for this channel to enable reset cards.')}
                  </AlertDescription>
                </Alert>
              )}
              {data.reset && (
                <>
                  <ResetCardRow
                    title={t('5-hour reset card')}
                    cards={data.reset.available_five_hour_resets}
                    lastUsedAt={
                      data.reset.latest_five_hour_reset_history?.used_at
                    }
                    resetType='FIVE_HOUR'
                    onUse={handleUseReset}
                    pending={usingReset === 'FIVE_HOUR'}
                    disabled={!props.channelId}
                  />
                  <ResetCardRow
                    title={t('Weekly reset card')}
                    cards={data.reset.available_week_resets}
                    lastUsedAt={data.reset.latest_week_reset_history?.used_at}
                    resetType='WEEK'
                    onUse={handleUseReset}
                    pending={usingReset === 'WEEK'}
                    disabled={!props.channelId}
                  />
                </>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </Dialog>
  )
}
