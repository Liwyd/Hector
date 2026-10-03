import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { API, notifyChanged, session } from '../api'
import { copyText, useAsync, useIsDesktop, useOnChanged } from '../hooks'
import { ago } from '../format'
import { SectionNav } from '../components/section'
import { ErrorPanel } from '../components/states'
import { TopBar } from '../components/topbar'
import { pad2, useConfirm } from '../components/resource'
import { useToast } from '../components/toast'
import type { QueueEntry } from '../types'

const statusTag: Record<QueueEntry['status'], { cls: string; label: string }> = {
  waiting: { cls: 'tag tag-warn', label: 'waiting for stock' },
  creating: { cls: 'tag tag-move', label: 'building' },
  done: { cls: 'tag tag-run', label: 'built' },
  failed: { cls: 'tag tag-red', label: 'failed' },
}

const statusSquare: Record<QueueEntry['status'], string> = {
  waiting: 'sq sq-warn',
  creating: 'sq sq-move',
  done: 'sq sq-run',
  failed: 'sq sq-err',
}

/** Time left until the next stock check. Zero time means "not scheduled" —
 *  a finished or failed order never counts down again. */
function countdown(target: string | undefined, now: number): string | null {
  if (!target || target.startsWith('0001')) return null
  const ms = new Date(target).getTime() - now
  if (!Number.isFinite(ms)) return null
  if (ms <= 0) return 'due now'
  const total = Math.round(ms / 1000)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${String(s).padStart(2, '0')}s`
  return `${s}s`
}

/**
 * 09 · Order queue — the out-of-stock list. Nothing here talks to Hetzner
 * directly: the backend owns the poll, this screen only shows where each
 * order stands and lets it be cancelled.
 */
export default function QueueScreen() {
  const desktop = useIsDesktop()
  const navigate = useNavigate()
  const toast = useToast()
  const confirm = useConfirm()

  const view = useAsync(() => API.queue.list(), [], { pollMs: 15000 })
  useOnChanged(() => view.reload(true))

  const entries = view.data?.entries ?? []
  const waiting = entries.filter((e) => e.status === 'waiting').length
  const [now, setNow] = useState(() => Date.now())
  const [cancelId, setCancelId] = useState('')
  const [revealed, setRevealed] = useState<Record<string, boolean>>({})
  const [busyId, setBusyId] = useState('')

  // one clock for every countdown on screen; nothing to tick when idle
  useEffect(() => {
    if (waiting === 0) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [waiting])

  // An order that turned into a server must reach the fleet list at once.
  const seen = useRef<Record<string, string>>({})
  useEffect(() => {
    for (const e of view.data?.entries ?? []) {
      const prev = seen.current[e.id]
      if (prev && prev !== e.status && e.status === 'done') {
        notifyChanged()
        toast.push({ kind: 'success', title: `${e.serverName || e.request.name} is built`, detail: 'It has left the queue.' })
      }
      seen.current[e.id] = e.status
    }
  }, [view.data, toast])

  const signOut = () => {
    session.clear()
    navigate('/signin', { replace: true })
  }

  const err = (e: unknown, title: string) =>
    toast.push({ kind: 'error', title, detail: e instanceof Error ? e.message : 'error' })

  const checkNow = async () => {
    setBusyId('__check__')
    try {
      await API.queue.check()
      view.reload(true)
    } catch (e) {
      err(e, 'Stock check failed')
    } finally {
      setBusyId('')
    }
  }

  const cancelOrder = async () => {
    const id = cancelId
    confirm.cancel()
    setCancelId('')
    if (!id) return
    try {
      await API.queue.remove(id)
      toast.push({ kind: 'success', title: 'Order cancelled' })
      view.reload(true)
    } catch (e) {
      err(e, 'Could not cancel the order')
    }
  }

  const forgetSecret = async (e: QueueEntry) => {
    try {
      await API.queue.forget(e.id)
      setRevealed((r) => ({ ...r, [e.id]: false }))
      view.reload(true)
    } catch (err2) {
      err(err2, 'Could not clear the password')
    }
  }

  const copySecret = async (e: QueueEntry) => {
    const ok = await copyText(e.rootPassword || '')
    toast.push({ kind: ok ? 'success' : 'error', title: ok ? 'Password copied' : 'Copy failed' })
  }

  const interval = view.data?.intervalSeconds ?? 300
  const lastPoll = view.data?.lastPollAt

  return (
    <div className={desktop ? 'page page--d' : 'page'}>
      <TopBar onRefresh={() => view.reload(true)} onSignOut={signOut} />
      <SectionNav />

      <section style={{ padding: desktop ? '26px 40px 0' : '14px 16px 0' }}>
        <div className="head" style={{ marginBottom: 14 }}>
          <h1 className="d" style={{ fontSize: desktop ? 56 : 46 }}>
            Order queue
          </h1>
          <span className={waiting ? 'd red' : 'd t3'} style={{ fontSize: desktop ? 56 : 46 }}>
            {pad2(waiting)}
          </span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
          <span className="m t3" style={{ fontSize: 11, letterSpacing: '.06em' }}>
            STOCK RE-CHECKED EVERY {Math.round(interval / 60)} MIN
            {lastPoll && !lastPoll.startsWith('0001') ? ` · LAST ${ago(lastPoll).toUpperCase()}` : ''}
          </span>
          <button
            type="button"
            className="btn btn-line"
            style={{ height: 32, padding: '0 12px', fontSize: 11, marginLeft: 'auto' }}
            disabled={busyId === '__check__'}
            onClick={() => void checkNow()}
          >
            {busyId === '__check__' ? 'Checking…' : 'Check now'}
          </button>
        </div>
      </section>

      <section style={{ padding: desktop ? '22px 40px 60px' : '16px 16px 60px' }}>
        {view.loading && !view.data ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {Array.from({ length: 3 }).map((_, i) => (
              <div key={i} className="skel" style={{ height: 76 }} />
            ))}
          </div>
        ) : view.error && !view.data ? (
          <ErrorPanel error={view.error} onRetry={() => view.reload(true)} />
        ) : entries.length === 0 ? (
          <div style={{ padding: desktop ? '40px 0' : '24px 0' }}>
            <p style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.02em', lineHeight: 1.2, margin: 0 }}>
              Nothing queued.
            </p>
            <p className="t2" style={{ fontSize: 14, lineHeight: 1.5, margin: '10px 0 0', maxWidth: 560 }}>
              When a server type is sold out in the New server screen, pick it and order it here instead. Hector re-checks
              stock every {Math.round(interval / 60)} minutes and builds it the moment it returns — with the image, name and
              settings you confirmed.
            </p>
            <button
              type="button"
              className="btn btn-line"
              style={{ height: 40, padding: '0 16px', fontSize: 12, marginTop: 18 }}
              onClick={() => navigate('/new')}
            >
              Open New server
            </button>
          </div>
        ) : (
          entries.map((e) => (
            <Row
              key={e.id}
              e={e}
              now={now}
              busy={busyId === e.id}
              revealed={!!revealed[e.id]}
              onReveal={() => setRevealed((r) => ({ ...r, [e.id]: !r[e.id] }))}
              onCancel={() => {
                setCancelId(e.id)
                confirm.ask()
              }}
              onCopy={() => void copySecret(e)}
              onForget={() => void forgetSecret(e)}
              onRemove={async () => {
                setBusyId(e.id)
                try {
                  await API.queue.remove(e.id)
                  view.reload(true)
                } catch (err2) {
                  err(err2, 'Could not remove the entry')
                } finally {
                  setBusyId('')
                }
              }}
            />
          ))
        )}

        {view.error && view.data && (
          <div style={{ marginTop: 16 }}>
            <ErrorPanel error={view.error} onRetry={() => view.reload(true)} />
          </div>
        )}
      </section>

      {confirm.dialog({
        eyebrow: 'Order queue',
        title: 'Cancel this order?',
        confirmLabel: 'Cancel order',
        onConfirm: () => void cancelOrder(),
        children: (
          <p className="t2" style={{ fontSize: 14, lineHeight: 1.5, margin: '10px 0 0' }}>
            The order stops being checked. Stock alerts already collected are kept — you can order the type again at any
            time.
          </p>
        ),
      })}
    </div>
  )
}

function Row({
  e,
  now,
  busy,
  revealed,
  onReveal,
  onCancel,
  onCopy,
  onForget,
  onRemove,
}: {
  e: QueueEntry
  now: number
  busy: boolean
  revealed: boolean
  onReveal: () => void
  onCancel: () => void
  onCopy: () => void
  onForget: () => void
  onRemove: () => void
}) {
  const tag = statusTag[e.status]
  const left = countdown(e.nextCheckAt, now)
  const scheduled = e.status === 'waiting' && left

  return (
    <div style={{ padding: '16px 0', borderBottom: '1px solid var(--line-row)' }}>
      <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
        <span className={statusSquare[e.status]} style={{ marginTop: 6 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span className="m" style={{ fontSize: 15, fontWeight: 600 }}>
              {e.request.name}
            </span>
            <span className={tag.cls}>{tag.label}</span>
            <span className="num t3" style={{ fontSize: 11 }}>
              {e.id}
            </span>
          </div>
          <div className="num t3" style={{ fontSize: 12, marginTop: 6 }}>
            {e.request.type.toUpperCase()} · {e.request.location.toUpperCase()} · {e.request.image}
          </div>
          <div className="t3" style={{ fontSize: 11.5, marginTop: 6 }}>
            QUEUED {ago(e.createdAt).toUpperCase()}
            {e.attempts > 0 ? ` · ${e.attempts} CHECK${e.attempts === 1 ? '' : 'S'}` : ''}
            {scheduled ? ` · NEXT IN ${left}` : ''}
            {e.status === 'done' && e.serverId ? ` · SERVER #${e.serverId}` : ''}
          </div>
          {e.lastError && (
            <div className="m" style={{ fontSize: 11, color: 'var(--accent-soft)', marginTop: 6, lineHeight: 1.5 }}>
              {e.lastError}
            </div>
          )}

          {e.status === 'done' && e.rootPassword && (
            <div
              style={{
                marginTop: 10,
                border: '1px solid var(--line2)',
                background: 'var(--field)',
                padding: '10px 12px',
                maxWidth: 460,
              }}
            >
              <div className="eb" style={{ fontSize: 10 }}>
                Root password
              </div>
              <div className="num" style={{ fontSize: 13, marginTop: 6, wordBreak: 'break-all' }}>
                {revealed ? e.rootPassword : '••••••••••••••••'}
              </div>
            </div>
          )}

          <div style={{ display: 'flex', gap: 18, flexWrap: 'wrap', marginTop: 12 }}>
            {e.status === 'waiting' && (
              <SmallAct disabled={busy} onClick={onCancel}>
                Cancel order
              </SmallAct>
            )}
            {e.status === 'done' && !!e.serverId && (
              <SmallAct onClick={onReveal}>{revealed ? 'Hide password' : 'Reveal password'}</SmallAct>
            )}
            {e.status === 'done' && revealed && (
              <>
                <SmallAct onClick={onCopy}>Copy password</SmallAct>
                <SmallAct onClick={onForget}>Forget password</SmallAct>
              </>
            )}
            {(e.status === 'failed' || e.status === 'done') && (
              <SmallAct disabled={busy} onClick={onRemove}>
                {busy ? 'Removing…' : 'Remove entry'}
              </SmallAct>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function SmallAct({ children, onClick, disabled }: { children: ReactNode; onClick: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      className="m t3"
      disabled={disabled}
      onClick={onClick}
      style={{
        fontSize: 11,
        letterSpacing: '.08em',
        textTransform: 'uppercase',
        height: 24,
        color: disabled ? 'var(--t4)' : undefined,
        cursor: disabled ? 'default' : 'pointer',
      }}
    >
      {children}
    </button>
  )
}
