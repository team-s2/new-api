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
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { GrokOAuthLoginDialog } from '../grok-oauth-login-dialog'

const authResponse = (id: string) => ({
  data: {
    success: true,
    data: { session_id: id, authorize_url: `https://auth.x.ai/${id}` },
  },
})

describe('Grok OAuth dialog', () => {
  test('reopening ignores the previous authorization response', async () => {
    let resolveOld!: (value: ReturnType<typeof authResponse>) => void
    const oldResponse = new Promise<ReturnType<typeof authResponse>>(
      (resolve) => {
        resolveOld = resolve
      }
    )
    vi.spyOn(api, 'post')
      .mockReturnValueOnce(oldResponse)
      .mockResolvedValueOnce(authResponse('new'))
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const props = { onOpenChange: vi.fn(), onApply: vi.fn() }
    const view = render(<GrokOAuthLoginDialog {...props} open />)
    view.rerender(<GrokOAuthLoginDialog {...props} open={false} />)
    view.rerender(<GrokOAuthLoginDialog {...props} open />)
    await screen.findByText('https://auth.x.ai/new')
    await act(async () => {
      resolveOld(authResponse('old'))
      await oldResponse
    })
    fireEvent.click(screen.getByRole('button', { name: 'Open authorize page' }))
    expect(open).toHaveBeenCalledWith('https://auth.x.ai/new', '_blank')
  })

  test('an exchange finishing after close cannot fill the reopened dialog', async () => {
    let resolveExchange!: (value: {
      data: { success: boolean; data: { credential: string } }
    }) => void
    const pending = new Promise<{
      data: { success: boolean; data: { credential: string } }
    }>((resolve) => {
      resolveExchange = resolve
    })
    vi.spyOn(api, 'post')
      .mockResolvedValueOnce(authResponse('first'))
      .mockReturnValueOnce(pending)
      .mockResolvedValueOnce(authResponse('second'))
    const props = { onOpenChange: vi.fn(), onApply: vi.fn() }
    const view = render(<GrokOAuthLoginDialog {...props} open />)
    await screen.findByText('https://auth.x.ai/first')
    fireEvent.change(screen.getByRole('textbox'), {
      target: { value: 'callback' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Complete login' }))
    view.rerender(<GrokOAuthLoginDialog {...props} open={false} />)
    view.rerender(<GrokOAuthLoginDialog {...props} open />)
    await screen.findByText('https://auth.x.ai/second')
    await act(async () => {
      resolveExchange({
        data: { success: true, data: { credential: 'old-credential' } },
      })
      await pending
    })
    expect(
      screen.queryByRole('button', { name: 'Fill credential into key field' })
    ).not.toBeInTheDocument()
    expect(props.onApply).not.toHaveBeenCalled()
  })

  test('a failed new authorization cannot reuse the old URL', async () => {
    vi.spyOn(api, 'post')
      .mockResolvedValueOnce(authResponse('first'))
      .mockResolvedValueOnce({
        data: { success: false, message: 'Unavailable' },
      })
    const props = { onOpenChange: vi.fn(), onApply: vi.fn() }
    const view = render(<GrokOAuthLoginDialog {...props} open />)
    await screen.findByText('https://auth.x.ai/first')
    view.rerender(<GrokOAuthLoginDialog {...props} open={false} />)
    view.rerender(<GrokOAuthLoginDialog {...props} open />)
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Open authorize page' })
      ).toBeDisabled()
    )
  })

  test('a successful exchange applies the credential', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce(authResponse('flow'))
      .mockResolvedValueOnce({
        data: { success: true, data: { credential: 'credential-json' } },
      })
    const props = { onOpenChange: vi.fn(), onApply: vi.fn() }
    render(<GrokOAuthLoginDialog {...props} open />)
    await screen.findByText('https://auth.x.ai/flow')
    fireEvent.change(screen.getByRole('textbox'), {
      target: { value: 'callback' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Complete login' }))
    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Fill credential into key field',
      })
    )
    expect(post).toHaveBeenLastCalledWith(
      '/api/channel/grok/oauth/exchange',
      { session_id: 'flow', input: 'callback' },
      expect.anything()
    )
    expect(props.onApply).toHaveBeenCalledWith('credential-json')
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })
})
