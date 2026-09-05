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
import { ChannelsProvider, useChannels } from '../../channels-provider'
import { BalanceQueryDialog } from '../balance-query-dialog'
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
    rerender(<BigModelOAuthControls channelType={62} onLogin={onLogin} />)
    fireEvent.click(screen.getByRole('button', { name: 'OAuth login' }))
    expect(onLogin).toHaveBeenCalledOnce()
  })
  test('opening type 62 queries Coding Plan usage and displays its account dialog', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: {} } })
    render(queryDialog(62, true))
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
      render(queryDialog(62, false))
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
