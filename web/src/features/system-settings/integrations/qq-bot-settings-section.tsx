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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'

const FLOW_STAGES = [
  { value: 'user', labelKey: 'User' },
  { value: 'node', labelKey: 'Node' },
  { value: 'token', labelKey: 'Token' },
  { value: 'group', labelKey: 'Group' },
  { value: 'model', labelKey: 'Model' },
  { value: 'channel', labelKey: 'Channel' },
] as const

const MIN_SECRET_LENGTH = 16

const createQQBotSchema = (t: (key: string) => string) =>
  z.object({
    enabled: z.boolean(),
    self_id: z
      .string()
      .trim()
      .regex(
        /^([1-9][0-9]*)?$/,
        t('The bot QQ number must be a positive integer')
      ),
    ws_secret: z
      .string()
      .trim()
      .refine(
        (value) => value === '' || value.length >= MIN_SECRET_LENGTH,
        t('The connection secret must be at least 16 characters')
      ),
    user_id: z.coerce.number().int().min(1),
    command_prefix: z
      .string()
      .trim()
      .regex(
        /^[^\s/]{1,32}$/,
        t(
          'The command prefix must be 1 to 32 characters without spaces or slashes'
        )
      ),
    sessions: z.string(),
    hidden_groups: z.string(),
    flow_stages: z
      .array(z.string())
      .min(2, t('Select at least two flow stages')),
    flow_hours: z.coerce.number().int().min(1),
    flow_top_n: z.coerce.number().int().min(1).max(100),
    flow_overflow: z.enum(['aggregate', 'hide']),
    request_timeout: z.coerce.number().int().min(1),
    font_path: z.string().trim(),
  })

type QQBotSchema = ReturnType<typeof createQQBotSchema>
type QQBotFormInput = z.input<QQBotSchema>
type QQBotFormValues = z.output<QQBotSchema>

export type QQBotDefaults = {
  'qq_bot.enabled': boolean
  'qq_bot.self_id': string
  'qq_bot.user_id': number
  'qq_bot.command_prefix': string
  'qq_bot.sessions': string
  'qq_bot.hidden_groups': string
  'qq_bot.flow_stages': string
  'qq_bot.flow_hours': number
  'qq_bot.flow_top_n': number
  'qq_bot.flow_overflow': 'aggregate' | 'hide'
  'qq_bot.request_timeout': number
  'qq_bot.font_path': string
}

const buildFormDefaults = (defaults: QQBotDefaults): QQBotFormInput => ({
  enabled: defaults['qq_bot.enabled'],
  self_id: defaults['qq_bot.self_id'],
  ws_secret: '',
  user_id: defaults['qq_bot.user_id'],
  command_prefix: defaults['qq_bot.command_prefix'],
  sessions: defaults['qq_bot.sessions'],
  hidden_groups: defaults['qq_bot.hidden_groups'],
  flow_stages: defaults['qq_bot.flow_stages'].split(',').filter(Boolean),
  flow_hours: defaults['qq_bot.flow_hours'],
  flow_top_n: defaults['qq_bot.flow_top_n'],
  flow_overflow: defaults['qq_bot.flow_overflow'],
  request_timeout: defaults['qq_bot.request_timeout'],
  font_path: defaults['qq_bot.font_path'],
})

export function QQBotSettingsSection({
  defaultValues,
}: {
  defaultValues: QQBotDefaults
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const schema = useMemo(() => createQQBotSchema(t), [t])
  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<QQBotFormInput, unknown, QQBotFormValues>({
    resolver: zodResolver(schema),
    defaultValues: formDefaults,
  })

  useResetForm(form, formDefaults)

  const onSubmit = async (values: QQBotFormValues) => {
    const stages = FLOW_STAGES.map((stage) => stage.value)
      .filter((stage) => values.flow_stages.includes(stage))
      .join(',')
    const next: Array<[keyof QQBotDefaults, string | number]> = [
      ['qq_bot.self_id', values.self_id],
      ['qq_bot.user_id', values.user_id],
      ['qq_bot.command_prefix', values.command_prefix],
      ['qq_bot.sessions', values.sessions],
      ['qq_bot.hidden_groups', values.hidden_groups],
      ['qq_bot.flow_stages', stages],
      ['qq_bot.flow_hours', values.flow_hours],
      ['qq_bot.flow_top_n', values.flow_top_n],
      ['qq_bot.flow_overflow', values.flow_overflow],
      ['qq_bot.request_timeout', values.request_timeout],
      ['qq_bot.font_path', values.font_path],
    ]
    const updates: Array<{ key: string; value: string | number | boolean }> =
      next
        .filter(([key, value]) => value !== defaultValues[key])
        .map(([key, value]) => ({ key, value }))
    // The saved secret is never sent to the browser, so empty means unchanged.
    if (values.ws_secret) {
      updates.push({ key: 'qq_bot.ws_secret', value: values.ws_secret })
    }
    // Last: the server checks enabling against the values saved above.
    if (values.enabled !== defaultValues['qq_bot.enabled']) {
      updates.push({ key: 'qq_bot.enabled', value: values.enabled })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }
    form.setValue('ws_secret', '')
  }

  return (
    <SettingsSection title={t('QQ Bot')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Point the NapCat reverse WebSocket client at /onebot/v11/ws on this server, using the secret below as its token.'
            )}
          </p>

          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable QQ bot')}</FormLabel>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <div className='grid grid-cols-1 gap-4 md:grid-cols-4'>
            <FormField
              control={form.control}
              name='self_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Bot QQ number')}</FormLabel>
                  <FormControl>
                    <Input inputMode='numeric' autoComplete='off' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='ws_secret'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Connection secret')}</FormLabel>
                  <FormControl>
                    <Input
                      type='password'
                      autoComplete='new-password'
                      placeholder={t('Leave empty to keep the saved secret')}
                      {...field}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='user_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Audit user ID')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Channel changes from the bot are logged under this user.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <FormField
            control={form.control}
            name='command_prefix'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Command prefix')}</FormLabel>
                <FormControl>
                  <Input autoComplete='off' {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Commands always start with a slash, for example /newapi quota for the prefix newapi. Chats that are not bound get no reply.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='sessions'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Bound chats')}</FormLabel>
                <FormControl>
                  <Textarea
                    rows={4}
                    placeholder={
                      'group:123456\ngroup:234567=paid\nprivate:10001'
                    }
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'One chat per line. Add =group to limit a chat to the channels and flow of that group. Anyone in a bound chat can run every command.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='hidden_groups'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Hidden group tags')}</FormLabel>
                <FormControl>
                  <Input placeholder='default,all' {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Comma-separated groups the quota image shows no tag for. Channel filtering is not affected.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='flow_stages'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Flow image stages')}</FormLabel>
                <div className='flex flex-wrap gap-4'>
                  {FLOW_STAGES.map((stage) => (
                    <label
                      key={stage.value}
                      className='flex items-center gap-2 text-sm'
                    >
                      <Checkbox
                        checked={field.value.includes(stage.value)}
                        onCheckedChange={(checked) =>
                          field.onChange(
                            checked
                              ? [...field.value, stage.value]
                              : field.value.filter(
                                  (value) => value !== stage.value
                                )
                          )
                        }
                      />
                      {t(stage.labelKey)}
                    </label>
                  ))}
                </div>
                <FormMessage />
              </FormItem>
            )}
          />

          <div className='grid grid-cols-1 gap-4 md:grid-cols-4'>
            <FormField
              control={form.control}
              name='flow_hours'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Default flow range (hours)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='flow_top_n'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Top N per stage')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      max={100}
                      step={1}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='flow_overflow'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Beyond Top N')}</FormLabel>
                  <Select
                    items={[
                      { value: 'aggregate', label: t('Merge into Other') },
                      { value: 'hide', label: t('Hide the whole path') },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='aggregate'>
                          {t('Merge into Other')}
                        </SelectItem>
                        <SelectItem value='hide'>
                          {t('Hide the whole path')}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='request_timeout'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Request timeout (seconds)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <FormField
            control={form.control}
            name='font_path'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Font file path')}</FormLabel>
                <FormControl>
                  <Input autoComplete='off' {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Optional TTF, OTF or TTC file with Chinese glyphs. Leave empty to use the Noto Sans CJK font in the Docker image.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
