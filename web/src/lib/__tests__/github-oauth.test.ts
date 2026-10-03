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
import { afterEach, expect, it, vi } from 'vitest'

import { buildGitHubOAuthUrl, buildOAuthAuthorizationUrl } from '../oauth'

afterEach(() => vi.unstubAllGlobals())

it('requests private organization membership access alongside email access', () => {
  const url = new URL(buildGitHubOAuthUrl('client', 'state'))
  expect(url.searchParams.get('scope')).toBe('user:email read:org')
  expect(url.searchParams.get('state')).toBe('state')
})

it.each(['http://127.0.0.1:6185', 'https://new-api.zjusec.net'])(
  'explicitly returns GitHub login, binding and verification to %s',
  (origin) => {
    vi.stubGlobal('window', { location: { origin } })
    const state = 'state+with&reserved=characters'
    const loginUrl = new URL(buildGitHubOAuthUrl('client', state))
    const accountUrl = new URL(
      buildOAuthAuthorizationUrl('github', state, {
        github_client_id: 'client',
      })
    )
    for (const url of [loginUrl, accountUrl]) {
      expect(url.origin + url.pathname).toBe(
        'https://github.com/login/oauth/authorize'
      )
      expect(url.searchParams.get('redirect_uri')).toBe(
        `${origin}/oauth/github`
      )
      expect(url.searchParams.get('client_id')).toBe('client')
      expect(url.searchParams.get('state')).toBe(state)
      expect(url.searchParams.get('scope')).toBe('user:email read:org')
    }
  }
)
