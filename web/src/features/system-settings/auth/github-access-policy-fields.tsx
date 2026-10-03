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
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import type { GitHubAccessForm } from './github-access-policy'

export function GitHubAccessPolicyFields() {
  const { t } = useTranslation()
  const form = useFormContext<{ githubAccess: GitHubAccessForm }>()
  const enabled = form.watch('githubAccess.enabled')
  return (
    <>
      <FormField
        control={form.control}
        name='githubAccess.enabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Restrict GitHub sign-in')}</FormLabel>
              <FormDescription>
                {t(
                  'Only listed GitHub users or active members of any listed organization may sign in or link GitHub. Empty lists deny everyone. Other login methods are unaffected.'
                )}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch checked={field.value} onCheckedChange={field.onChange} />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />
      <FormField
        control={form.control}
        name='githubAccess.organizations'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Allowed GitHub organizations')}</FormLabel>
            <FormControl>
              <Textarea
                {...field}
                disabled={!enabled}
                placeholder='team-one, team-two'
              />
            </FormControl>
            <FormDescription>
              {t(
                'Separate organization names with commas or newlines. Each organization may need to approve the OAuth app for read:org access.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name='githubAccess.user_ids'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Allowed GitHub user IDs')}</FormLabel>
            <FormControl>
              <Textarea
                {...field}
                disabled={!enabled}
                placeholder='123456, 789012'
              />
            </FormControl>
            <FormDescription>
              {t(
                'Use permanent numeric account IDs, separated by commas or newlines. Find the id field at https://api.github.com/users/USERNAME. Usernames can be reassigned.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name='githubAccess.role'
        render={({ field }) => (
          <FormItem className='lg:col-span-2'>
            <FormLabel>{t('Automatic GitHub role')}</FormLabel>
            <FormControl>
              <NativeSelect {...field} disabled={!enabled}>
                <NativeSelectOption value='0'>
                  {t('Keep existing role (new users are regular users)')}
                </NativeSelectOption>
                <NativeSelectOption value='10'>{t('Admin')}</NativeSelectOption>
                <NativeSelectOption value='100'>
                  {t('Root (full access)')}
                </NativeSelectOption>
              </NativeSelect>
            </FormControl>
            <FormDescription>
              {t(
                'Promote allowed users after successful GitHub login, including existing accounts. Higher roles are preserved. Disabling promotion does not revoke previously granted roles.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <p className='text-muted-foreground text-sm lg:col-span-2'>
        {t(
          'Membership is checked at GitHub sign-in. Changes do not revoke existing sessions or access through other login methods. Revoke sessions and adjust roles separately when removing access.'
        )}
      </p>
    </>
  )
}
