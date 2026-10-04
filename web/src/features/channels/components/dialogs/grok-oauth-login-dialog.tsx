/*
Copyright (C) 2023-2026 QuantumNous and the new-api contributors.

This program is free software: you can redistribute it under the terms
of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { ExternalLink, Loader2, LogIn } from 'lucide-react'
import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { exchangeGrokOAuthCode, startGrokOAuthLogin } from '../../api'
import { CHANNEL_TYPE_GROK_SUB } from '../../constants'

type GrokOAuthLoginDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onApply: (credential: string) => void
}

export function GrokOAuthLoginDialog(props: GrokOAuthLoginDialogProps) {
  const { t } = useTranslation()
  const [authorizeUrl, setAuthorizeUrl] = useState('')
  const [sessionId, setSessionId] = useState('')
  const [pasted, setPasted] = useState('')
  const [credential, setCredential] = useState('')
  const [loadingUrl, setLoadingUrl] = useState(false)
  const [exchanging, setExchanging] = useState(false)

  const flowController = useRef<AbortController | null>(null)
  const onStartError = useEffectEvent((error: unknown) => {
    handleServerError(error, t('Failed to start OAuth login'))
  })

  useEffect(() => {
    if (!props.open) return
    const controller = new AbortController()
    flowController.current = controller
    setAuthorizeUrl('')
    setSessionId('')
    setPasted('')
    setCredential('')
    setExchanging(false)
    setLoadingUrl(true)
    startGrokOAuthLogin(controller.signal)
      .then((res) => {
        if (controller.signal.aborted) return
        if (!res.success || !res.data) {
          throw createServerError(res)
        }
        setAuthorizeUrl(res.data.authorize_url)
        setSessionId(res.data.session_id)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingUrl(false)
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) onStartError(error)
      })
    return () => controller.abort()
  }, [props.open])

  const handleExchange = async () => {
    const controller = flowController.current
    if (exchanging || !sessionId || !controller || controller.signal.aborted) {
      return
    }
    setExchanging(true)
    try {
      const res = await exchangeGrokOAuthCode(
        sessionId,
        pasted.trim(),
        controller.signal
      )
      if (controller.signal.aborted) return
      if (!res.success || !res.data) {
        throw createServerError(res, t('OAuth login failed'))
      }
      setCredential(res.data.credential)
    } catch (error) {
      if (!controller.signal.aborted) {
        handleServerError(error, t('OAuth login failed'))
      }
    } finally {
      if (!controller.signal.aborted) setExchanging(false)
    }
  }

  const handleApply = () => {
    props.onApply(credential)
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={credential ? t('OAuth login succeeded') : t('Grok OAuth login')}
      description={t(
        'Log in on x.ai in your browser, then paste the redirected callback URL back here.'
      )}
    >
      {credential ? (
        <div className='flex flex-col gap-4'>
          <Alert>
            <AlertDescription>
              {t(
                'The Grok subscription credential JSON has been generated. Tokens are refreshed automatically while the refresh token stays valid.'
              )}
            </AlertDescription>
          </Alert>
          <Button type='button' onClick={handleApply}>
            <LogIn className='mr-2 h-4 w-4' />
            {t('Fill credential into key field')}
          </Button>
        </div>
      ) : (
        <div className='flex flex-col gap-4'>
          <div className='flex flex-col gap-2'>
            <div className='text-sm font-medium'>
              {t('Step 1: Open the authorize URL and log in')}
            </div>
            {loadingUrl ? (
              <div className='text-muted-foreground flex items-center gap-2 text-sm'>
                <Loader2 className='h-4 w-4 animate-spin' />
                {t('Generating authorize URL...')}
              </div>
            ) : (
              <div className='flex flex-wrap items-center gap-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => window.open(authorizeUrl, '_blank')}
                  disabled={!authorizeUrl}
                >
                  <ExternalLink className='mr-2 h-4 w-4' />
                  {t('Open authorize page')}
                </Button>
              </div>
            )}
            <div className='text-muted-foreground rounded-md border p-2 font-mono text-xs break-all'>
              {authorizeUrl}
            </div>
          </div>

          <div className='flex flex-col gap-2'>
            <div className='text-sm font-medium'>
              {t('Step 2: Paste the callback URL')}
            </div>
            <div className='text-muted-foreground text-xs'>
              {t(
                'After login the browser will be redirected to http://127.0.0.1:56121/callback and the page will fail to load — that is expected. Copy the full URL from the address bar and paste it below.'
              )}
            </div>
            <Textarea
              rows={3}
              placeholder='http://127.0.0.1:56121/callback?code=...'
              aria-label={t('Step 2: Paste the callback URL')}
              value={pasted}
              onChange={(event) => setPasted(event.target.value)}
            />
            <Button
              type='button'
              onClick={handleExchange}
              disabled={
                loadingUrl || !sessionId || exchanging || !pasted.trim()
              }
            >
              {exchanging ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : (
                <LogIn className='mr-2 h-4 w-4' />
              )}
              {exchanging ? t('Exchanging...') : t('Complete login')}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}

export function GrokOAuthControls(props: {
  channelType: number
  onLogin: () => void
}) {
  const { t } = useTranslation()
  if (props.channelType !== CHANNEL_TYPE_GROK_SUB) return null
  return (
    <div className='border-border/60 flex flex-col gap-3 border-y py-4'>
      <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
        <div className='text-muted-foreground text-xs'>
          {t(
            'Grok Subscription channels use an OAuth credential as the key. Use OAuth login to obtain it.'
          )}
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={props.onLogin}
        >
          <LogIn className='mr-2 h-4 w-4' />
          {t('OAuth login')}
        </Button>
      </div>
      <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
        <AlertDescription>
          {t(
            "Disclaimer: Personal use only. Do not distribute or share any credentials. This channel relays your Grok subscription through the Grok CLI gateway; use it only if you understand the flow and risks, and comply with xAI's terms and policies."
          )}
        </AlertDescription>
      </Alert>
    </div>
  )
}
