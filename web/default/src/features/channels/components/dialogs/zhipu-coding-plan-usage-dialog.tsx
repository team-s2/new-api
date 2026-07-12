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
import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
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

import type {
  ZhipuCodingPlanUsageLimit,
  ZhipuCodingPlanUsageResponse,
} from '../../api'

type ZhipuCodingPlanUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName: string
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
  const percentage = clampPercentage(props.limit?.percentage)
  let usageText = `${percentage}%`
  if (
    typeof props.limit?.current_value === 'number' &&
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
          <span className='text-2xl font-semibold'>{usageText}</span>
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

export function ZhipuCodingPlanUsageDialog(
  props: ZhipuCodingPlanUsageDialogProps
) {
  const { t } = useTranslation()
  const data = props.response?.data

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
        </>
      )}
    </Dialog>
  )
}
