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
import * as z from 'zod'

export const defaultGitHubAccessPolicy =
  '{"enabled":false,"organizations":[],"user_ids":[],"role":0}'

const entries = (value: string): string[] =>
  value.split(/[\s,]+/).filter(Boolean)

export const githubAccessSchema = z.object({
  enabled: z.boolean(),
  organizations: z.string().refine((value) => {
    const names = entries(value)
    return (
      names.length <= 100 &&
      names.every((name) =>
        /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$/.test(name)
      )
    )
  }, 'Enter valid GitHub organization names (up to 100).'),
  user_ids: z.string().refine((value) => {
    const ids = entries(value)
    return (
      ids.length <= 1000 &&
      ids.every(
        (id) =>
          id.length <= 19 &&
          /^[1-9][0-9]*$/.test(id) &&
          BigInt(id) <= 9223372036854775807n
      )
    )
  }, 'Enter positive numeric GitHub account IDs (up to 1000).'),
  role: z.enum(['0', '10', '100']),
})

export type GitHubAccessForm = z.infer<typeof githubAccessSchema>

const storedPolicySchema = z.object({
  enabled: z.boolean(),
  organizations: z.array(z.string()),
  user_ids: z.array(z.string()),
  role: z.union([z.literal(0), z.literal(10), z.literal(100)]),
})

export function parseGitHubAccessPolicy(raw?: string): GitHubAccessForm {
  try {
    const policy = storedPolicySchema.parse(
      JSON.parse(raw || defaultGitHubAccessPolicy)
    )
    return {
      enabled: policy.enabled,
      organizations: policy.organizations.join('\n'),
      user_ids: policy.user_ids.join('\n'),
      role: String(policy.role) as GitHubAccessForm['role'],
    }
  } catch {
    // Invalid persisted policy is denied by the server; keep the form fail-closed.
    return { enabled: true, organizations: '', user_ids: '', role: '0' }
  }
}

export function serializeGitHubAccessPolicy(value: GitHubAccessForm): string {
  return JSON.stringify({
    enabled: value.enabled,
    organizations: [
      ...new Set(
        entries(value.organizations).map((name) => name.toLowerCase())
      ),
    ],
    user_ids: [...new Set(entries(value.user_ids))],
    role: value.enabled ? Number(value.role) : 0,
  })
}
