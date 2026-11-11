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
import { toIntlLocale } from '@/i18n/languages'
import { formatPercent } from '@/lib/format'
import { getServerMessage } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import type { OpenCodeGoUsageResponse, OpenCodeGoUsageWindow } from '../../api'

type OpenCodeGoUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  response: OpenCodeGoUsageResponse | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

function clampPercent(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? Math.max(0, Math.min(100, parsed)) : 0
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

function UsageWindowCard(props: {
  title: string
  description: string
  window?: OpenCodeGoUsageWindow
}) {
  const { t, i18n } = useTranslation()
  const hasData = props.window != null
  const percent = clampPercent(props.window?.percent)
  const variant = percentVariant(percent)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  let resetText = '-'
  if (props.window?.resets_at) {
    const resetAt = new Date(props.window.resets_at)
    if (!Number.isNaN(resetAt.getTime())) {
      resetText = new Intl.DateTimeFormat(locale, {
        dateStyle: 'medium',
        timeStyle: 'short',
      }).format(resetAt)
    }
  }

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-sm font-semibold'>
              {props.title}
            </CardTitle>
            <CardDescription className='mt-1 text-xs'>
              {props.description}
            </CardDescription>
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={cn(
                'text-xl leading-none font-semibold tabular-nums',
                hasData
                  ? percentTextClassName[variant]
                  : 'text-muted-foreground'
              )}
            >
              {hasData ? formatPercent(percent) : '-'}
            </div>
            <div className='text-muted-foreground mt-1 text-[11px]'>
              {t('Used')}
            </div>
          </div>
        </div>
        {props.window?.status === 'rate-limited' ? (
          <StatusBadge
            label={t('Rate limited')}
            variant='danger'
            copyable={false}
            size='sm'
          />
        ) : null}
      </CardHeader>
      <CardContent className='p-3 pt-0'>
        {hasData ? (
          <>
            <Progress
              value={percent}
              aria-label={`${props.title}: ${formatPercent(percent)}`}
              className='mt-1'
            />
            <div className='text-muted-foreground mt-2 text-[11px]'>
              {t('Resets at:')} {resetText}
            </div>
          </>
        ) : (
          <div className='text-muted-foreground mt-1 text-sm'>-</div>
        )}
      </CardContent>
    </Card>
  )
}

export function OpenCodeGoUsageDialog(props: OpenCodeGoUsageDialogProps) {
  const { t, i18n } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const [showRawJson, setShowRawJson] = useState(false)

  const usage = props.response?.data ?? null
  const errorMessage =
    props.response?.success === false
      ? getServerMessage(props.response) || t('Failed to fetch usage')
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

  let fetchedAt = '-'
  if (usage?.fetched_at) {
    const parsed = new Date(usage.fetched_at)
    if (!Number.isNaN(parsed.getTime())) {
      fetchedAt = new Intl.DateTimeFormat(
        toIntlLocale(i18n.resolvedLanguage || i18n.language),
        { dateStyle: 'medium', timeStyle: 'medium' }
      ).format(parsed)
    }
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          setShowRawJson(false)
        }
        props.onOpenChange(nextOpen)
      }}
      title={t('OpenCode Go Subscription Usage')}
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
              <StatusBadge
                label={t('OpenCode Go Subscription')}
                variant='purple'
                copyable={false}
              />
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
                  {t('Fetched at')}
                </div>
                <div className='mt-1 text-xs tabular-nums'>{fetchedAt}</div>
              </div>
            </div>
          </CardContent>
        </Card>

        <div className='grid grid-cols-1 gap-3 md:grid-cols-3'>
          <UsageWindowCard
            title={t('5-hour window')}
            description={t('Rolling quota')}
            window={usage?.rolling}
          />
          <UsageWindowCard
            title={t('Weekly window')}
            description={t('Calendar week')}
            window={usage?.weekly}
          />
          <UsageWindowCard
            title={t('Monthly window')}
            description={t('Calendar month')}
            window={usage?.monthly}
          />
        </div>

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
