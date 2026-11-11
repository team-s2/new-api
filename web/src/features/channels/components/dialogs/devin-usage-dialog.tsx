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
import { ScrollArea } from '@/components/ui/scroll-area'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { getServerMessage } from '@/lib/server-error-message'

import type { DevinUsageResponse } from '../../api'

type DevinUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  response: DevinUsageResponse | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

export function DevinUsageDialog(props: DevinUsageDialogProps) {
  const { t } = useTranslation()
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

  return (
    <Dialog
      open={props.open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          setShowRawJson(false)
        }
        props.onOpenChange(nextOpen)
      }}
      title={t('Devin Subscription Usage')}
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
              {usage?.plan_name ? (
                <StatusBadge
                  label={usage.plan_name}
                  variant='purple'
                  copyable={false}
                />
              ) : null}
              {usage?.pro ? (
                <StatusBadge label='Pro' variant='info' copyable={false} />
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
                  {t('Account')}
                </div>
                <div className='mt-1 text-xs break-all'>
                  {usage?.account_display_name ||
                    usage?.name ||
                    usage?.email ||
                    usage?.user_id ||
                    '-'}
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {usage?.models && usage.models.length > 0 ? (
          <Card size='sm' className='gap-0 py-0'>
            <CardHeader className='p-3 pb-2'>
              <CardTitle className='text-sm font-semibold'>
                {t('SWE-2 models')}
              </CardTitle>
              <CardDescription className='text-xs'>
                {t('{{count}} catalog entries total', {
                  count: usage.total_catalog_entries ?? 0,
                })}
              </CardDescription>
            </CardHeader>
            <CardContent className='flex flex-col gap-2 p-3 pt-0'>
              {usage.models.map((model) => (
                <div
                  key={model.model_uid}
                  className='bg-background ring-border/60 flex items-center justify-between gap-3 rounded-lg px-3 py-2 ring-1'
                >
                  <div className='min-w-0'>
                    <div className='font-mono text-xs break-all'>
                      {model.model_uid}
                    </div>
                    {model.label ? (
                      <div className='text-muted-foreground text-[11px]'>
                        {model.label}
                      </div>
                    ) : null}
                  </div>
                  <div className='flex shrink-0 flex-wrap items-center justify-end gap-1'>
                    {model.disabled ? (
                      <StatusBadge
                        label={t('Disabled')}
                        variant='neutral'
                        copyable={false}
                      />
                    ) : null}
                    {model.is_premium ? (
                      <StatusBadge
                        label={t('Premium')}
                        variant='warning'
                        copyable={false}
                      />
                    ) : null}
                    {model.supports_images ? (
                      <StatusBadge
                        label={t('Images')}
                        variant='info'
                        copyable={false}
                      />
                    ) : null}
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>
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
