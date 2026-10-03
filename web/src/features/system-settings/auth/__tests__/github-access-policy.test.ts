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
import { describe, expect, it } from 'vitest'

import {
  githubAccessSchema,
  parseGitHubAccessPolicy,
  serializeGitHubAccessPolicy,
} from '../github-access-policy'

describe('GitHub access policy', () => {
  it('normalizes separators and duplicate entries without losing large account IDs', () => {
    expect(
      JSON.parse(
        serializeGitHubAccessPolicy({
          enabled: true,
          organizations: 'One, two\nONE',
          user_ids: '42 42,9223372036854775807',
          role: '10',
        })
      )
    ).toEqual({
      enabled: true,
      organizations: ['one', 'two'],
      user_ids: ['42', '9223372036854775807'],
      role: 10,
    })
  })
  it.each(['../admin', '-invalid', 'a'.repeat(40)])(
    'rejects invalid organization %s',
    (organizations) => {
      expect(
        githubAccessSchema.safeParse({
          enabled: true,
          organizations,
          user_ids: '',
          role: '0',
        }).success
      ).toBe(false)
    }
  )
  it.each(['01', '0', '-1', 'alice', '9223372036854775808'])(
    'rejects invalid account ID %s',
    (user_ids) => {
      expect(
        githubAccessSchema.safeParse({
          enabled: true,
          organizations: '',
          user_ids,
          role: '0',
        }).success
      ).toBe(false)
    }
  )
  it('renders invalid persisted settings as deny-all instead of unrestricted access', () => {
    expect(parseGitHubAccessPolicy('broken')).toEqual({
      enabled: true,
      organizations: '',
      user_ids: '',
      role: '0',
    })
  })
})
