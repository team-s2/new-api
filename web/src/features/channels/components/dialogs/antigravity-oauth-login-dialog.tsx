import { ExternalLink, KeyRound, Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import {
  exchangeAntigravityOAuthCode,
  startAntigravityOAuthLogin,
} from '../../api'
import { CHANNEL_TYPE_ANTIGRAVITY_SUB } from '../../constants'

type AntigravityOAuthLoginDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onApply: (credential: string) => void
}

export function AntigravityOAuthLoginDialog({
  open,
  onOpenChange,
  onApply,
}: AntigravityOAuthLoginDialogProps) {
  const { t } = useTranslation()
  const [callbackInput, setCallbackInput] = useState('')
  const [sessionId, setSessionId] = useState('')
  const [isOpeningLogin, setIsOpeningLogin] = useState(false)
  const [isExchanging, setIsExchanging] = useState(false)
  const requestAbortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!open) {
      requestAbortRef.current?.abort()
      requestAbortRef.current = null
      setCallbackInput('')
      setSessionId('')
      setIsOpeningLogin(false)
      setIsExchanging(false)
    }
  }, [open])

  useEffect(() => {
    return () => {
      requestAbortRef.current?.abort()
      requestAbortRef.current = null
    }
  }, [])

  const handleOpenLogin = async () => {
    requestAbortRef.current?.abort()
    const controller = new AbortController()
    requestAbortRef.current = controller
    setIsOpeningLogin(true)
    try {
      const res = await startAntigravityOAuthLogin(controller.signal)
      if (!res.success || !res.data?.authorize_url || !res.data?.session_id) {
        throw createServerError(res, t('Failed to start OAuth login'))
      }
      setSessionId(res.data.session_id)
      window.open(res.data.authorize_url, '_blank', 'noopener,noreferrer')
    } catch (error) {
      if (controller.signal.aborted) return
      handleServerError(error, t('Failed to start OAuth login'))
    } finally {
      if (requestAbortRef.current === controller) {
        requestAbortRef.current = null
        setIsOpeningLogin(false)
      }
    }
  }

  const handleExchange = async () => {
    const trimmed = callbackInput.trim()
    if (!trimmed) {
      toast.error(t('Please paste the OAuth callback URL'))
      return
    }
    if (!sessionId) {
      toast.error(
        t('Please click Open Antigravity Login Page first to start a session')
      )
      return
    }

    requestAbortRef.current?.abort()
    const controller = new AbortController()
    requestAbortRef.current = controller
    setIsExchanging(true)
    try {
      const res = await exchangeAntigravityOAuthCode(
        sessionId,
        trimmed,
        controller.signal
      )
      if (!res.success || !res.data?.credential) {
        throw createServerError(res, t('Failed to exchange OAuth token'))
      }
      onApply(res.data.credential)
      toast.success(t('Antigravity OAuth credential filled in'))
      onOpenChange(false)
    } catch (error) {
      if (controller.signal.aborted) return
      handleServerError(error, t('Failed to exchange OAuth token'))
    } finally {
      if (requestAbortRef.current === controller) {
        requestAbortRef.current = null
        setIsExchanging(false)
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-xl'>
        <DialogHeader>
          <DialogTitle>{t('Antigravity OAuth Login')}</DialogTitle>
          <DialogDescription>
            {t(
              'Sign in with your Google account in the browser and paste the callback URL to generate the Antigravity subscription credential JSON.'
            )}
          </DialogDescription>
        </DialogHeader>

        <div className='space-y-4'>
          <div className='bg-muted/40 space-y-3 rounded-lg border p-3.5 text-sm'>
            <p className='font-medium'>{t('Steps')}</p>
            <ol className='text-muted-foreground list-inside list-decimal space-y-1.5 text-xs leading-relaxed'>
              <li>
                {t(
                  'Click "Open Antigravity Login Page" below and sign in with your Google account.'
                )}
              </li>
              <li>
                {t(
                  'After authorizing, the browser will redirect to http://localhost:51121/oauth-callback?code=...&state=... (the page will fail to load).'
                )}
              </li>
              <li>
                {t(
                  'Copy the full URL from the browser address bar and paste it below.'
                )}
              </li>
            </ol>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={handleOpenLogin}
              disabled={isOpeningLogin || isExchanging}
            >
              {isOpeningLogin ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : (
                <ExternalLink className='mr-2 h-4 w-4' />
              )}
              {t('Open Antigravity Login Page')}
            </Button>
          </div>

          <div className='space-y-2'>
            <Label htmlFor='antigravity-oauth-callback'>
              {t('Callback URL')}
            </Label>
            <Textarea
              id='antigravity-oauth-callback'
              rows={3}
              value={callbackInput}
              onChange={(e) => setCallbackInput(e.target.value)}
              placeholder='http://localhost:51121/oauth-callback?code=...&state=...'
              className='font-mono text-xs'
              disabled={isExchanging}
            />
          </div>

          <Alert>
            <AlertDescription className='text-xs'>
              {t(
                'The authorization code is exchanged for an access_token, refresh_token, and Cloud Code Assist project_id, which New API refreshes automatically.'
              )}
            </AlertDescription>
          </Alert>
        </div>

        <DialogFooter>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
            disabled={isExchanging}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={handleExchange}
            disabled={isExchanging || !callbackInput.trim() || !sessionId}
          >
            {isExchanging && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
            {t('Exchange & Fill Key')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function AntigravityOAuthControls(props: {
  channelType: number
  onLogin: () => void
}) {
  const { t } = useTranslation()
  if (props.channelType !== CHANNEL_TYPE_ANTIGRAVITY_SUB) return null

  return (
    <div className='border-border/60 flex flex-col gap-3 border-y py-4'>
      <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
        <div className='text-muted-foreground text-xs'>
          {t(
            'Antigravity Subscription channels use a Google OAuth JSON credential (access_token + refresh_token + project_id).'
          )}
        </div>
        <Button type='button' variant='outline' size='sm' onClick={props.onLogin}>
          <KeyRound className='mr-2 h-4 w-4' />
          {t('OAuth Login')}
        </Button>
      </div>
    </div>
  )
}
