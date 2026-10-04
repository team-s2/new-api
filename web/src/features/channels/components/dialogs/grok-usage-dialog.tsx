/*
Copyright (C) 2023-2026 QuantumNous

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
import { Check, ChevronDown, ChevronUp, Copy, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatPercent } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { GrokBillingWindow, GrokUsageResponse } from '../../api'

type GrokUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  response: GrokUsageResponse | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

function clampPercent(value: unknown): number {
  const v = Number(value)
  return Number.isFinite(v) ? Math.max(0, Math.min(100, v)) : 0
}

function percentVariant(percent: number): 'danger' | 'warning' | 'info' {
  if (percent >= 95) {
    return 'danger'
  }
  if (percent >= 80) {
    return 'warning'
  }
  return 'info'
}

const percentTextClassName: Record<'danger' | 'warning' | 'info', string> = {
  danger: 'text-destructive',
  warning: 'text-warning',
  info: 'text-info',
}

function formatMoney(value: unknown): string {
  const v = Number(value)
  return Number.isFinite(v)
    ? formatCurrencyFromUSD(v, {
        digitsLarge: 2,
        digitsSmall: 2,
        abbreviate: false,
        showSymbol: true,
      })
    : '-'
}

function formatPeriod(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '-'
  }
  return value
}

function BillingWindowCard(props: {
  title: string
  window?: GrokBillingWindow | null
}) {
  const { t } = useTranslation()
  const hasData =
    !!props.window && Object.keys(props.window as object).length > 0
  const percent = clampPercent(
    props.window?.usage_percent ?? props.window?.used_percent
  )
  const variant = percentVariant(percent)
  const showMoney =
    props.window?.monthly_limit != null || props.window?.monthly_used != null

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-sm font-semibold'>
              {props.title}
            </CardTitle>
            <CardDescription className='mt-1 text-xs'>
              {t('Period:')}{' '}
              {hasData ? formatPeriod(props.window?.period_start) : '-'} →{' '}
              {hasData ? formatPeriod(props.window?.period_end) : '-'}
            </CardDescription>
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={cn(
                'text-xl leading-none font-semibold tabular-nums',
                hasData ? percentTextClassName[variant] : 'text-muted-foreground'
              )}
            >
              {hasData ? formatPercent(percent) : '-'}
            </div>
            <div className='text-muted-foreground mt-1 text-[11px]'>
              {t('Used')}
            </div>
          </div>
        </div>
        {showMoney ? (
          <CardDescription className='text-xs'>
            {t('Monthly usage:')} {formatMoney(props.window?.monthly_used)} /{' '}
            {formatMoney(props.window?.monthly_limit)}
          </CardDescription>
        ) : null}
      </CardHeader>
      <CardContent className='p-3 pt-0'>
        {hasData ? (
          <Progress
            value={percent}
            aria-label={`${props.title}: ${formatPercent(percent)}`}
            className='mt-1'
          />
        ) : (
          <div className='text-muted-foreground mt-1 text-sm'>-</div>
        )}
      </CardContent>
    </Card>
  )
}

export function GrokUsageDialog(props: GrokUsageDialogProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const [showRawJson, setShowRawJson] = useState(false)

  const usage = props.response?.data ?? null
  const errorMessage =
    props.response?.success === false
      ? props.response?.message?.trim() || t('Failed to fetch usage')
      : ''

  const channelLabelName = props.channelDisplayName ?? props.channelName ?? '-'
  let channelLabelId = ''
  if (props.channelDisplayId != null) {
    channelLabelId = ` (#${props.channelDisplayId})`
  } else if (props.channelId) {
    channelLabelId = ` (#${props.channelId})`
  }
  const channelLabel = `${channelLabelName}${channelLabelId}`

  const rawJsonText = useMemo(() => {
    if (!props.response) {
      return ''
    }
    try {
      return JSON.stringify(props.response, null, 2)
    } catch {
      return String(props.response?.data ?? '')
    }
  }, [props.response])

  return (
    <Dialog
      open={props.open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          setShowRawJson(false)
        }
        props.onOpenChange(nextOpen)
      }}
      title={t('Grok Subscription Usage')}
      contentClassName='sm:max-w-[700px]'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => props.onOpenChange(false)}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-col gap-4'>
        {errorMessage ? (
          <div className='rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-400'>
            {errorMessage}
          </div>
        ) : null}

        <Card size='sm' className='bg-muted/30 gap-0 py-0'>
          <CardHeader className='p-4 pb-2'>
            <CardTitle className='text-muted-foreground text-xs font-medium'>
              {t('Account Status')}
            </CardTitle>
            {props.onRefresh ? (
              <CardAction>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={props.onRefresh}
                  disabled={Boolean(props.isRefreshing)}
                >
                  <RefreshCw data-icon='inline-start' />
                  {t('Refresh')}
                </Button>
              </CardAction>
            ) : null}
          </CardHeader>
          <CardContent className='p-4 pt-0'>
            <div className='flex flex-wrap items-center gap-2'>
              {usage?.plan ? (
                <StatusBadge
                  label={usage.plan}
                  variant='purple'
                  copyable={false}
                />
              ) : null}
              <StatusBadge
                label={`HTTP ${props.response?.upstream_status ?? '-'}`}
                variant='neutral'
                copyable={false}
              />
            </div>
            <div className='mt-4 grid grid-cols-1 gap-3 md:grid-cols-2'>
              <div className='bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1'>
                <div className='text-muted-foreground text-[11px] font-medium'>
                  {t('Channel')}
                </div>
                <div className='mt-1 text-xs break-all'>{channelLabel}</div>
              </div>
              <div className='bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1'>
                <div className='text-muted-foreground text-[11px] font-medium'>
                  {t('Prepaid balance')}
                </div>
                <div className='mt-1 text-xs font-semibold tabular-nums'>
                  {formatMoney(usage?.prepaid_balance)}
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
          <BillingWindowCard
            title={t('Weekly Credits Window')}
            window={usage?.weekly}
          />
          <BillingWindowCard
            title={t('Monthly Window')}
            window={usage?.monthly}
          />
        </div>

        {usage?.product_usage && usage.product_usage.length > 0 ? (
          <div className='flex flex-col gap-2'>
            <div className='text-sm font-semibold'>{t('Product Usage')}</div>
            <div className='flex flex-col gap-2'>
              {usage.product_usage.map((product) => {
                const percent = clampPercent(product.usage_percent)
                return (
                  <div
                    key={product.product}
                    className='bg-background ring-border/60 flex items-center justify-between gap-3 rounded-lg px-3 py-2 ring-1'
                  >
                    <span className='font-mono text-xs break-all'>
                      {product.product}
                    </span>
                    <span className='shrink-0 text-xs font-semibold tabular-nums'>
                      {formatPercent(percent)}
                    </span>
                  </div>
                )
              })}
            </div>
          </div>
        ) : null}

        <Collapsible
          open={showRawJson}
          onOpenChange={setShowRawJson}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-center justify-between gap-2 p-3 transition-colors'
                aria-expanded={showRawJson}
              />
            }
          >
            <div className='text-sm font-medium'>{t('Raw JSON')}</div>
            {showRawJson ? (
              <ChevronUp className='text-muted-foreground h-4 w-4' />
            ) : (
              <ChevronDown className='text-muted-foreground h-4 w-4' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <>
              <div className='flex justify-end border-t px-3 py-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => copyToClipboard(rawJsonText)}
                  disabled={!rawJsonText}
                >
                  {copiedText === rawJsonText ? (
                    <Check data-icon='inline-start' className='text-success' />
                  ) : (
                    <Copy data-icon='inline-start' />
                  )}
                  {t('Copy')}
                </Button>
              </div>
              <ScrollArea className='max-h-[50vh]'>
                <pre className='bg-muted/30 m-0 p-3 text-xs break-words whitespace-pre-wrap'>
                  {rawJsonText || '-'}
                </pre>
              </ScrollArea>
            </>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </Dialog>
  )
}
