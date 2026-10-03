import { useEffect, useLayoutEffect, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { ErrorBoundary } from './components/boundary'
import { session } from './api'
import SignIn from './screens/SignIn'
import Servers from './screens/Servers'
import ServerDetail from './screens/ServerDetail'
import Rescale from './screens/Rescale'
import NewServer from './screens/NewServer'
import Created from './screens/Created'
import QueueScreen from './screens/Queue'
import Storage from './screens/resources/Storage'
import ImagesScreen from './screens/resources/Images'
import NetworkScreen from './screens/resources/Network'
import FirewallsScreen from './screens/resources/Firewalls'
import { FloatingIPsScreen, PrimaryIPsScreen } from './screens/resources/Addresses'
import LoadBalancersScreen from './screens/resources/LoadBalancers'
import { CertificatesScreen, SSHKeysScreen } from './screens/resources/Access'
import PlacementScreen from './screens/resources/Placement'
import ActivityScreen from './screens/resources/Activity'

/** Every new section is one route here and one SECTIONS entry in section.tsx. */
const guarded = (el: React.ReactNode) => <RequireAuth>{el}</RequireAuth>

/** The order queue is the one screen that changes the room: a pale yellow
 *  curtain wipes up over it, and the colours swap to paper underneath the
 *  curtain. Leaving runs the same wipe backwards. `idle` is off screen,
 *  `rise` / `fall` are moving, `rest` is up and the page already owns it. */
type Wash = 'idle' | 'rise' | 'rest' | 'fall'
const isQueuePath = (p: string) => p === '/queue' || p.startsWith('/queue/')
const RISE_MS = 640 // matches the .62s wipe, plus a frame of slack
const FALL_MS = 600 // matches the .58s wipe
const noWipe = () =>
  typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
/** where the wipe goes next: up over a screen that is opening, down off one
 *  that is closing, nowhere if motion is off or nothing is on screen */
const nextWash = (opening: boolean, from: Wash): Wash =>
  noWipe() ? (opening ? 'rest' : 'idle') : opening ? 'rise' : from === 'idle' ? 'idle' : 'fall'

function RequireAuth({ children }: { children: React.ReactNode }) {
  if (!session.get()) return <Navigate to="/signin" replace />
  return <>{children}</>
}

export default function App() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const queue = isQueuePath(pathname)
  const [wash, setWash] = useState<Wash>('idle')
  const [was, setWas] = useState(queue)

  // Flipping the wipe during render (not in an effect) puts the new route and
  // the new wipe in one commit: no frame is ever painted with the new screen
  // in the old screen's colours. The layout effect below then lands the paper
  // swap before the browser paints.
  if (queue !== was) {
    setWas(queue)
    setWash(nextWash(queue, wash))
  }

  // The paper swap has to land in the same paint as the curtain hiding, or the
  // page is caught mid-switch — a layout effect, so it runs before the paint.
  useLayoutEffect(() => {
    document.documentElement.toggleAttribute('data-paper', wash === 'rest')
  }, [wash])

  // Opening straight on /queue (reload, deep link) still gets the wipe: the
  // first frame is the screen as it was, then the curtain goes up over it.
  useEffect(() => {
    if (queue && wash === 'idle') setWash(noWipe() ? 'rest' : 'rise')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Each moving wipe ends on its own: up → paper, down → out of the way.
  useEffect(() => {
    if (wash !== 'rise' && wash !== 'fall') return
    const t = window.setTimeout(() => setWash(wash === 'rise' ? 'rest' : 'idle'), wash === 'rise' ? RISE_MS : FALL_MS)
    return () => window.clearTimeout(t)
  }, [wash])

  // Ctrl/Cmd+A selects the top-most open dialog/sheet (or the page), never
  // the list sitting behind it. Inputs keep their own select-all.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.key === 'a' || e.key === 'A') || !(e.ctrlKey || e.metaKey) || e.altKey) return
      const el = document.activeElement
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || (el as HTMLElement | null)?.isContentEditable) return
      const dialogs = document.querySelectorAll<HTMLElement>('[role="dialog"]')
      const scope = dialogs.length ? dialogs[dialogs.length - 1] : document.getElementById('root')
      if (!scope) return
      e.preventDefault()
      window.getSelection()?.selectAllChildren(scope)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Any 401 with code "session" (expired token) drops the user back to sign-in.
  useEffect(() => {
    const onExpired = () => {
      session.clear()
      navigate('/signin', { replace: true })
    }
    window.addEventListener('hector:session-expired', onExpired)
    return () => window.removeEventListener('hector:session-expired', onExpired)
  }, [navigate])

  return (
    <ErrorBoundary resetKey={pathname}>
    <div className={`queue-wash q-${wash}`} aria-hidden="true" />
    <Routes>
      <Route path="/signin" element={session.get() ? <Navigate to="/" replace /> : <SignIn />} />
      <Route
        path="/"
        element={
          <RequireAuth>
            <Servers />
          </RequireAuth>
        }
      />
      <Route
        path="/servers/:id"
        element={
          <RequireAuth>
            <ServerDetail />
          </RequireAuth>
        }
      />
      <Route
        path="/servers/:id/:tab"
        element={
          <RequireAuth>
            <ServerDetail />
          </RequireAuth>
        }
      />
      <Route
        path="/servers/:id/rescale"
        element={
          <RequireAuth>
            <Rescale />
          </RequireAuth>
        }
      />
      <Route
        path="/servers/:id/created"
        element={
          <RequireAuth>
            <Created />
          </RequireAuth>
        }
      />
      <Route
        path="/new"
        element={
          <RequireAuth>
            <NewServer />
          </RequireAuth>
        }
      />
      <Route path="/queue" element={guarded(<QueueScreen />)} />
      <Route path="/storage" element={guarded(<Storage />)} />
      <Route path="/images" element={guarded(<ImagesScreen />)} />
      <Route path="/network" element={guarded(<NetworkScreen />)} />
      <Route path="/network/firewalls" element={guarded(<FirewallsScreen />)} />
      <Route path="/addresses" element={guarded(<FloatingIPsScreen />)} />
      <Route path="/addresses/primary" element={guarded(<PrimaryIPsScreen />)} />
      <Route path="/load-balancers" element={guarded(<LoadBalancersScreen />)} />
      <Route path="/access" element={guarded(<SSHKeysScreen />)} />
      <Route path="/access/certificates" element={guarded(<CertificatesScreen />)} />
      <Route path="/placement-groups" element={guarded(<PlacementScreen />)} />
      <Route path="/activity" element={guarded(<ActivityScreen />)} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
    </ErrorBoundary>
  )
}
