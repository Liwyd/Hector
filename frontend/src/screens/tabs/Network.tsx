import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { API, Firewalls, FloatingIPs, Networks } from '../../api'
import { copyText, useAsync } from '../../hooks'
import { Ic } from '../../icons'
import { Sec } from '../../components/ui'
import { useToast } from '../../components/toast'
import { Act, Field, Select, Sheet } from '../../components/resource'
import type { ActionResult, ServerDetail } from '../../types'

/**
 * Network tab — everything this server is addressed by and filtered by:
 * public addresses, reverse DNS, floating IPs, private networks and the
 * firewalls applied to it. Attach and detach run from here too; the same
 * actions are available in the Addresses and Network sections.
 */
export default function Network({ detail, desktop, onChanged }: { detail: ServerDetail; desktop?: boolean; onChanged: () => void }) {
  const toast = useToast()
  const px = desktop ? 28 : 16
  const [rev, setRev] = useState(0)
  const [pane, setPane] = useState<'' | 'fip' | 'net'>('')

  const fips = useAsync(() => FloatingIPs.list(), [rev])
  const nets = useAsync(() => Networks.list(), [rev])
  const fws = useAsync(() => Firewalls.list(), [rev])

  const mine = (fips.data ?? []).filter((f) => f.serverId === detail.id)
  const freeFips = (fips.data ?? []).filter((f) => f.serverId == null)
  const mineNets = (nets.data ?? []).filter((n) => n.servers.includes(detail.id))
  const freeNets = (nets.data ?? []).filter((n) => !n.servers.includes(detail.id))
  const mineFws = (fws.data ?? []).filter((f) => f.appliedTo.some((t) => t.type === 'server' && t.serverId === detail.id))

  const copy = (label: string, value: string) => {
    void copyText(value).then((ok) => toast.push(ok ? { kind: 'success', title: `${label} copied` } : { kind: 'error', title: 'Copy failed', detail: value }))
  }

  const refresh = () => {
    setRev((r) => r + 1)
    onChanged()
  }

  /** Dispatch one action, track it to completion, then refresh this tab. */
  const run = async (title: string, call: Promise<ActionResult>) => {
    try {
      const res = await call
      toast.trackAction(title, res.action, refresh)
      return true
    } catch (err) {
      toast.push({ kind: 'error', title: `${title} failed`, detail: err instanceof Error ? err.message : 'error' })
      return false
    }
  }

  return (
    <>
      <section style={{ padding: `20px ${px}px 0` }}>
        <Sec n="01" title="Public" />
        <div className="card">
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 6px 12px 14px', borderBottom: '1px solid var(--line)' }}>
            <span className="eb" style={{ width: 34 }}>IPv4</span>
            <span className="m" style={{ flex: 1, fontSize: 15 }}>{detail.ipv4 || 'No IPv4'}</span>
            {detail.ipv4 && (
              <button className="btn btn-ico btn-bare" aria-label="Copy IPv4" onClick={() => copy('IPv4', detail.ipv4)}>
                <Ic.Copy />
              </button>
            )}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 6px 12px 14px' }}>
            <span className="eb" style={{ width: 34 }}>IPv6</span>
            <span className="m" style={{ flex: 1, fontSize: 15 }}>{detail.ipv6 || 'No IPv6'}</span>
            {detail.ipv6 && (
              <button className="btn btn-ico btn-bare" aria-label="Copy IPv6" onClick={() => copy('IPv6', detail.ipv6)}>
                <Ic.Copy />
              </button>
            )}
          </div>
        </div>
      </section>

      <section style={{ padding: `28px ${px}px 0` }}>
        <Sec n="02" title="Reverse DNS" />
        <div className="card">
          {detail.rdns.map((row, i) => (
            <RdnsRow
              key={`${row.family}-${row.ip}`}
              serverId={detail.id}
              family={row.family}
              ip={row.ip}
              value={row.dnsPtr}
              last={i === detail.rdns.length - 1}
              onSaved={onChanged}
            />
          ))}
          {detail.rdns.length === 0 && (
            <p className="m t3" style={{ fontSize: 11, padding: 14, margin: 0 }}>
              No public IP — nothing to point.
            </p>
          )}
        </div>
        <p className="m t3" style={{ fontSize: 10.5, margin: '10px 0 0', lineHeight: 1.6 }}>
          PTR RECORDS. LEAVE A FIELD EMPTY TO RESET IT TO THE HETZNER DEFAULT.
        </p>
      </section>

      <section style={{ padding: `28px ${px}px 0` }}>
        <Sec
          n="03"
          title="Floating IPs"
          right={<SmallAct onClick={() => setPane('fip')}>Attach</SmallAct>}
        />
        <div className="card">
          {mine.map((f, i) => (
            <Row key={f.id} last={i === mine.length - 1}>
              <span className="num" style={{ flex: 1, fontSize: 14 }}>{f.ip}</span>
              <span className="tag">{f.type}</span>
              <SmallAct onClick={() => void run(`Detach ${f.ip}`, FloatingIPs.act(f.id, 'unassign', {}))}>
                Detach
              </SmallAct>
            </Row>
          ))}
          {mine.length === 0 && (
            <p className="m t3" style={{ fontSize: 11, padding: 14, margin: 0 }}>
              {fips.error
                ? 'Could not read the floating IP list.'
                : freeFips.length
                  ? `${freeFips.length} unassigned floating IP(s) ready to attach.`
                  : 'No floating IP on this server. Create one in Addresses, then attach it here.'}
            </p>
          )}
        </div>
        <p className="m t3" style={{ fontSize: 10.5, margin: '10px 0 0', lineHeight: 1.6 }}>
          A FLOATING IP MOVES BETWEEN SERVERS WITHOUT CHANGING DNS.
        </p>
      </section>

      <section style={{ padding: `28px ${px}px 0` }}>
        <Sec
          n="04"
          title="Private networks"
          right={<SmallAct onClick={() => setPane('net')}>Attach</SmallAct>}
        />
        <div className="card">
          {mineNets.map((n, i) => (
            <Row key={n.id} last={i === mineNets.length - 1}>
              <span style={{ flex: 1, fontSize: 14 }}>{n.name}</span>
              <span className="num t3">{n.ipRange}</span>
              <SmallAct
                onClick={() =>
                  void run(`Detach from ${n.name}`, Networks.act(n.id, 'remove_server', { serverId: detail.id }))
                }
              >
                Detach
              </SmallAct>
            </Row>
          ))}
          {mineNets.length === 0 && (
            <p className="m t3" style={{ fontSize: 11, padding: 14, margin: 0 }}>
              {nets.error
                ? 'Could not read the network list.'
                : freeNets.length
                  ? `${freeNets.length} network(s) this server can join.`
                  : 'No private network yet. Create one in Network, then attach this server.'}
            </p>
          )}
        </div>
        <p className="m t3" style={{ fontSize: 10.5, margin: '10px 0 0', lineHeight: 1.6 }}>
          A PRIVATE NETWORK NEEDS A SUBNET IN THIS LOCATION BEFORE A SERVER CAN JOIN IT.
        </p>
      </section>

      <section style={{ padding: `28px ${px}px 40px` }}>
        <Sec
          n="05"
          title="Firewalls"
          right={
            <Link className="btn btn-line" style={{ height: 30, padding: '0 12px', fontSize: 11, letterSpacing: '.1em' }} to="/network/firewalls">
              Manage
            </Link>
          }
        />
        <div className="card">
          {mineFws.map((f, i) => (
            <Row key={f.id} last={i === mineFws.length - 1}>
              <span style={{ flex: 1, fontSize: 14 }}>{f.name}</span>
              <span className="t3" style={{ fontSize: 11 }}>{f.rules.length} rules</span>
            </Row>
          ))}
          {mineFws.length === 0 && (
            <p className="m t3" style={{ fontSize: 11, padding: 14, margin: 0 }}>
              {fws.error ? 'Could not read the firewall list.' : 'No firewall applied to this server.'}
            </p>
          )}
        </div>
        <p className="m t3" style={{ fontSize: 10.5, margin: '10px 0 0', lineHeight: 1.6 }}>
          FIREWALLS ARE APPLIED AND REMOVED FROM THE FIREWALLS SECTION.
        </p>
      </section>

      {pane === 'fip' && (
        <Sheet open onClose={() => setPane('')} eyebrow="Addresses" title="Attach floating IP" sub={detail.name}>
          {freeFips.length === 0 ? (
            <p className="m t3" style={{ fontSize: 12, margin: 0, lineHeight: 1.7 }}>
              No unassigned floating IP in the project. Create one in Addresses and it will show up here.
            </p>
          ) : (
            <AttachSelect
              label="Floating IP"
              hint="Only unassigned addresses are listed. This one keeps pointing at the server until you move it."
              options={freeFips.map((f) => ({ value: String(f.id), label: `${f.ip}${f.name ? ` · ${f.name}` : ''}` }))}
              onClose={() => setPane('')}
              onSubmit={(v) => run('Attach floating IP', FloatingIPs.act(Number(v), 'assign', { serverId: detail.id }))}
            />
          )}
        </Sheet>
      )}

      {pane === 'net' && (
        <Sheet open onClose={() => setPane('')} eyebrow="Network" title="Attach private network" sub={detail.name}>
          {freeNets.length === 0 ? (
            <p className="m t3" style={{ fontSize: 12, margin: 0, lineHeight: 1.7 }}>
              No network available. Create one in Network — with a subnet in this server's location — and it will show up here.
            </p>
          ) : (
            <AttachSelect
              label="Network"
              hint="The server gets a second interface inside this range."
              options={freeNets.map((n) => ({ value: String(n.id), label: `${n.name} · ${n.ipRange}` }))}
              onClose={() => setPane('')}
              onSubmit={(v) =>
                run(`Attach to network`, Networks.act(Number(v), 'add_server', { serverId: detail.id }))
              }
            />
          )}
        </Sheet>
      )}
    </>
  )
}

function Row({ children, last }: { children: React.ReactNode; last?: boolean }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '12px 14px',
        borderBottom: last ? undefined : '1px solid var(--line)',
      }}
    >
      {children}
    </div>
  )
}

/** Small inline action — deliberately always enabled so nothing in the tab
 *  sits there greyed out; the sheet explains it when there is nothing to pick. */
function SmallAct({ children, onClick }: { children: React.ReactNode; onClick: () => void }) {
  return (
    <button
      type="button"
      className="btn btn-line"
      style={{ height: 30, padding: '0 12px', fontSize: 11, letterSpacing: '.1em' }}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

function AttachSelect({
  label,
  hint,
  options,
  onClose,
  onSubmit,
}: {
  label: string
  hint: string
  options: { value: string; label: string }[]
  onClose: () => void
  onSubmit: (value: string) => Promise<boolean>
}) {
  const [pick, setPick] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (!pick || busy) return
    setBusy(true)
    const ok = await onSubmit(pick)
    setBusy(false)
    if (ok) onClose()
  }

  return (
    <div>
      <Field label={label} hint={hint}>
        <Select
          value={pick}
          onChange={setPick}
          options={[{ value: '', label: 'Choose…' }, ...options]}
          disabled={busy}
        />
      </Field>
      <div style={{ display: 'flex', gap: 8 }}>
        <Act tone="primary" disabled={!pick || busy} onClick={() => void submit()}>
          {busy ? 'Attaching…' : 'Attach'}
        </Act>
        <Act onClick={onClose}>Cancel</Act>
      </div>
    </div>
  )
}

function RdnsRow({
  serverId,
  family,
  ip,
  value,
  last,
  onSaved,
}: {
  serverId: number
  family: string
  ip: string
  value: string
  last: boolean
  onSaved: () => void
}) {
  const toast = useToast()
  const [draft, setDraft] = useState(value)
  const [busy, setBusy] = useState(false)
  // Hetzner rejects values that start or end with a dash or dot — trim the
  // ends on save instead of showing an error while typing.
  const cleaned = draft.trim().replace(/^[.-]+/, '').replace(/[.-]+$/, '')
  const dirty = cleaned !== value

  // follow the server's value after a save lands (poll/reload)
  useEffect(() => setDraft(value), [value])

  const save = async () => {
    if (!dirty || busy) return
    setBusy(true)
    try {
      // empty = reset to Hetzner's default PTR (the API takes null)
      const res = await API.action(serverId, 'change_dns_ptr', { ip, dns_ptr: cleaned || null })
      // the toast reports success/failure when the action settles — not before
      toast.trackAction(`Updating reverse DNS · ${ip}`, res.action)
      onSaved()
    } catch (err) {
      toast.push({ kind: 'error', title: 'Reverse DNS failed', detail: err instanceof Error ? err.message : 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ padding: '12px 14px', borderBottom: last ? undefined : '1px solid var(--line)' }}>
      <div className="eb" style={{ marginBottom: 8 }}>
        {family} · {ip}
      </div>
      <form
        className="field"
        style={{ height: 44 }}
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <input
          value={draft}
          placeholder="no rDNS"
          aria-label={`Reverse DNS for ${ip}`}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => setDraft(e.target.value)}
        />
        <button
          type="submit"
          className="m"
          style={{ fontSize: 11, letterSpacing: '.12em', color: dirty ? 'var(--accent)' : 'var(--t4)', height: 44 }}
          disabled={!dirty || busy}
        >
          SAVE
        </button>
      </form>
    </div>
  )
}
