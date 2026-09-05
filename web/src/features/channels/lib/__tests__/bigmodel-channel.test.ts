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
import { describe, expect, test } from 'vitest'

import {
  CHANNEL_TYPE_OPTIONS,
  channelTypeOptionsForTaskPluginBind,
} from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import { getChannelTypeIcon, getKeyPromptForType } from '../channel-utils'

const credential = JSON.stringify({
  api_key: 'coding-plan-key',
  access_token: 'oauth-token',
})
function channelForm(type: number, key = credential) {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Coding Plan',
    type,
    key,
    models: 'glm-5.3',
  }
}

describe('BigModel Coding Plan channel identity', () => {
  test('type 62 exposes Coding Plan metadata without task-plugin permission', () => {
    expect(
      CHANNEL_TYPE_OPTIONS.find((option) => option.value === 62)?.label
    ).toBe('BigModel Subscription (Coding Plan)')
    expect(
      channelTypeOptionsForTaskPluginBind(false).some(
        (option) => option.value === 62
      )
    ).toBe(true)
    expect(getChannelTypeIcon(62)).toBe('Zhipu')
    expect(getKeyPromptForType(62)).toContain('OAuth')
  })
  test('type 62 rejects a plain key and batch creation but accepts OAuth JSON', () => {
    expect(channelFormSchema.safeParse(channelForm(62)).success).toBe(true)
    expect(
      channelFormSchema.safeParse(channelForm(62, 'plain-key')).success
    ).toBe(false)
    expect(
      channelFormSchema.safeParse({
        ...channelForm(62),
        multi_key_mode: 'batch',
      }).success
    ).toBe(false)
  })
  test('type 61 accepts task-plugin credentials without Coding Plan validation', () => {
    expect(
      channelFormSchema.safeParse({
        ...channelForm(61, 'plugin-key'),
        base_url: 'https://plugin.example',
        task_plugin_key: 'plugin',
      }).success
    ).toBe(true)
  })
  test('editing a converted channel without entering a new key preserves stored credentials', () => {
    const form = channelForm(62, '')
    expect(channelFormSchema.safeParse(form).success).toBe(true)
    const payload = transformFormDataToUpdatePayload(form, 15)
    expect(payload.type).toBe(62)
    expect(payload).not.toHaveProperty('key')
  })
})
