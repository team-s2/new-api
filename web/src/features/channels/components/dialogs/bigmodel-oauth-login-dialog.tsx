/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it under the terms
of the GNU Affero General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any
later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { ExternalLink, Loader2, LogIn } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

import { exchangeZhipuOAuthToken, startZhipuOAuthLogin } from '../../api'
import { CHANNEL_TYPE_BIGMODEL_SUB } from '../../constants'

type BigModelOAuthLoginDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onApply: (credential: string) => void
}

export function BigModelOAuthLoginDialog({
  open,
  onOpenChange,
  onApply,
}: BigModelOAuthLoginDialogProps) {
  const { t } = useTranslation()
  const [authorizeUrl, setAuthorizeUrl] = useState('')
  const [state, setState] = useState('')
  const [pasted, setPasted] = useState('')
  const [username, setUsername] = useState('')
  const [credential, setCredential] = useState('')
  const [loadingUrl, setLoadingUrl] = useState(false)
  const [exchanging, setExchanging] = useState(false)

  useEffect(() => {
    if (!open) return
    setPasted('')
    setUsername('')
    setCredential('')
    setLoadingUrl(true)
    startZhipuOAuthLogin()
      .then((res) => {
        if (!res.success || !res.data) {
          throw new Error(res.message || t('Failed to start OAuth login'))
        }
        setAuthorizeUrl(res.data.authorize_url)
        setState(res.data.state)
      })
      .finally(() => setLoadingUrl(false))
      .catch((error) => {
        toast.error(
          error instanceof Error
            ? error.message
            : t('Failed to start OAuth login')
        )
      })
  }, [open, t])

  const handleExchange = async () => {
    if (exchanging) return
    setExchanging(true)
    try {
      const res = await exchangeZhipuOAuthToken(pasted.trim(), state)
      if (!res.success || !res.data) {
        throw new Error(res.message || t('OAuth login failed'))
      }
      setCredential(res.data.credential)
      setUsername(res.data.username || '')
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t('OAuth login failed')
      )
    } finally {
      setExchanging(false)
    }
  }

  const handleApply = () => {
    onApply(credential)
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        credential ? t('OAuth login succeeded') : t('BigModel OAuth login')
      }
      description={t(
        'Log in on bigmodel.cn in your browser, then paste the callback link back here.'
      )}
    >
      {credential ? (
        <div className='flex flex-col gap-4'>
          <Alert>
            <AlertDescription>
              {t('Logged in as {{name}}.', {
                name: username || 'unknown',
              })}{' '}
              {t(
                'The credential JSON has been generated, including the coding plan API key.'
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
              {t('Step 2: Paste the callback link')}
            </div>
            <div className='text-muted-foreground text-xs'>
              {t(
                'After login the browser will be redirected to a zcode:// link and the system may ask to open ZCode — cancel it, then copy the full link from the address bar and paste it below.'
              )}
            </div>
            <Textarea
              rows={3}
              placeholder='zcode://oauth/callback?authCode=...'
              value={pasted}
              onChange={(event) => setPasted(event.target.value)}
            />
            <Button
              type='button'
              onClick={handleExchange}
              disabled={exchanging || !pasted.trim()}
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

export function BigModelOAuthControls(props: {
  channelType: number
  onLogin: () => void
}) {
  const { t } = useTranslation()
  if (props.channelType !== CHANNEL_TYPE_BIGMODEL_SUB) return null
  return (
    <div className='border-border/60 flex flex-col gap-3 border-y py-4'>
      <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
        <div className='text-muted-foreground text-xs'>
          {t(
            'BigModel Subscription channels use an OAuth credential as the key. Use OAuth login to obtain it.'
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
            "Disclaimer: Personal use only. Do not distribute or share any credentials. This channel relays your BigModel Coding Plan subscription; use it only if you understand the flow and risks, and comply with Zhipu's terms and policies."
          )}
        </AlertDescription>
      </Alert>
    </div>
  )
}
