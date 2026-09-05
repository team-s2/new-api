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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useEffect } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { channelSchema } from '../../../types'
import { CHANNEL_TYPE_BIGMODEL_SUB } from '../../../constants'
import { ChannelsProvider, useChannels } from '../../channels-provider'
import { BalanceQueryDialog } from '../balance-query-dialog'
import { ZhipuCodingPlanUsageDialog } from '../zhipu-coding-plan-usage-dialog'
import { BigModelOAuthControls } from '../bigmodel-oauth-login-dialog'

function SelectChannel(props: { type: number; open: boolean }) {
  const { setCurrentRow } = useChannels()
  useEffect(() => {
    setCurrentRow(
      channelSchema.parse({
        id: 15,
        type: props.type,
        name: 'Coding Plan account',
        key: '',
        status: 1,
        created_time: 0,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
      })
    )
  }, [props.type, setCurrentRow])
  return <BalanceQueryDialog open={props.open} onOpenChange={() => undefined} />
}

function queryDialog(type: number, open: boolean) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return (
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <SelectChannel type={type} open={open} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

describe('BigModel channel controls', () => {
  test('switching from Task Plugin to Coding Plan exposes a working OAuth button', () => {
    const onLogin = vi.fn()
    const { rerender } = render(
      <BigModelOAuthControls channelType={61} onLogin={onLogin} />
    )
    expect(
      screen.queryByRole('button', { name: 'OAuth login' })
    ).not.toBeInTheDocument()
    rerender(<BigModelOAuthControls channelType={CHANNEL_TYPE_BIGMODEL_SUB} onLogin={onLogin} />)
    fireEvent.click(screen.getByRole('button', { name: 'OAuth login' }))
    expect(onLogin).toHaveBeenCalledOnce()
  })
  test('opening type 100 queries Coding Plan usage and displays its account dialog', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: {} } })
    render(queryDialog(CHANNEL_TYPE_BIGMODEL_SUB, true))
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/channel/15/zhipu/coding-plan/usage',
        expect.anything()
      )
    )
    expect(
      await screen.findByRole('dialog', {
        name: 'Zhipu Coding Plan Account Info',
      })
    ).toBeInTheDocument()
  })
  test('a closed Coding Plan dialog does not query usage', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: {} } })
    await act(async () => {
      render(queryDialog(CHANNEL_TYPE_BIGMODEL_SUB, false))
    })
    expect(get).not.toHaveBeenCalled()
  })
  test('Task Plugin opens ordinary balance controls without querying Coding Plan', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: {} } })
    render(queryDialog(61, true))
    expect(
      await screen.findByRole('dialog', { name: 'Query Balance' })
    ).toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })
})

describe('Zhipu usage dialog reset cards', () => {
  const usageWithReset = {
    success: true,
    data: {
      level: 'max',
      reset: {
        available_five_hour_resets: [{ expire_at: 1786600000000 }],
        available_week_resets: [],
        latest_five_hour_reset_history: null,
        latest_week_reset_history: null,
        has_unread_history: false,
      },
    },
  }

  test('renders reset cards and keeps empty windows unusable', async () => {
    render(
      <ZhipuCodingPlanUsageDialog
        open
        onOpenChange={() => undefined}
        channelName='coding plan'
        channelId={15}
        response={usageWithReset}
        onRefresh={() => undefined}
        isRefreshing={false}
      />
    )
    expect(await screen.findByText('Reset cards')).toBeInTheDocument()
    expect(screen.getByText('5-hour reset card')).toBeInTheDocument()
    expect(screen.getByText('Weekly reset card')).toBeInTheDocument()
    const buttons = screen.getAllByRole('button', { name: 'Apply reset' })
    expect(buttons).toHaveLength(2)
    expect(buttons[0]).not.toBeDisabled()
    expect(buttons[1]).toBeDisabled()
  })

  test('using a reset card posts the window type and refreshes', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: { used: true } } })
    const onRefresh = vi.fn()
    render(
      <ZhipuCodingPlanUsageDialog
        open
        onOpenChange={() => undefined}
        channelName='coding plan'
        channelId={15}
        response={usageWithReset}
        onRefresh={onRefresh}
        isRefreshing={false}
      />
    )
    fireEvent.click(
      screen.getAllByRole('button', { name: 'Apply reset' })[0]
    )
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/channel/15/zhipu/coding-plan/reset',
        { reset_type: 'FIVE_HOUR' },
        expect.anything()
      )
    )
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
  })

  test('shows an error alert when reset data is unavailable', async () => {
    render(
      <ZhipuCodingPlanUsageDialog
        open
        onOpenChange={() => undefined}
        channelName='coding plan'
        channelId={15}
        response={{
          success: true,
          data: {
            level: 'max',
            reset_unavailable_reason:
              'zhipu coding plan: credential has no zcode JWT',
          },
        }}
        onRefresh={() => undefined}
        isRefreshing={false}
      />
    )
    expect(await screen.findByText('Reset cards')).toBeInTheDocument()
    expect(
      screen.getByText('Reset card query failed')
    ).toBeInTheDocument()
    expect(
      screen.getByText(/credential has no zcode JWT/)
    ).toBeInTheDocument()
  })
})

describe('Zhipu usage dialog credit plans', () => {
  // Credit-based plans (2026-07-30 onward): 5-hour and weekly windows carry
  // credit usage, and no monthly MCP window exists.
  const creditPlanUsage = {
    success: true,
    data: {
      level: 'lite',
      five_hour: {
        usage: 2000,
        current_value: 69,
        remaining: 1930,
        percentage: 3,
        next_reset_time: 1790851347795,
        unit: 'credits' as const,
      },
      weekly: {
        usage: 10000,
        current_value: 73,
        remaining: 9926,
        percentage: 1,
        next_reset_time: 1791001173997,
        unit: 'credits' as const,
      },
      reset: {
        available_five_hour_resets: [],
        available_week_resets: [],
        latest_five_hour_reset_history: null,
        latest_week_reset_history: null,
        has_unread_history: false,
      },
    },
  }

  test('renders credit usage with its unit and marks MCP as not included', async () => {
    render(
      <ZhipuCodingPlanUsageDialog
        open
        onOpenChange={() => undefined}
        channelName='coding plan'
        channelId={20}
        response={creditPlanUsage}
        onRefresh={() => undefined}
        isRefreshing={false}
      />
    )
    expect(await screen.findByText('69 / 2,000')).toBeInTheDocument()
    expect(screen.getByText('73 / 10,000')).toBeInTheDocument()
    expect(screen.getAllByText('credits')).toHaveLength(2)
    expect(screen.getByText('Not included in this plan')).toBeInTheDocument()
  })
})
