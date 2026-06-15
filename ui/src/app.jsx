import { useState, useEffect } from 'preact/hooks'
import { fetchConfig, fetchZombies, fetchContainers, fetchSnapshot } from './api.js'
import { Login } from './components/Login.jsx'
import { ServiceTable } from './components/ServiceTable.jsx'
import { ContainerList } from './components/ContainerList.jsx'

const POLL_MS      = 30_000  // full analysis + containers (normal)
const SNAP_MS      = 5_000   // lightweight snapshot (normal)
const LIVE_POLL_MS = 10_000  // full analysis (live mode)
const LIVE_SNAP_MS = 1_000   // snapshot (live mode)

export function App() {
  const [cfg, setCfg] = useState(null)
  const [authed, setAuthed] = useState(false)
  const [needLogin, setNeedLogin] = useState(false)
  const [tab, setTab] = useState('zombies')
  const [zombies, setZombies] = useState(null)
  const [containers, setContainers] = useState(null)
  const [snapshots, setSnapshots] = useState({})
  const [loading, setLoading] = useState(true)
  const [reloading, setReloading] = useState(false)
  const [liveMode, setLiveMode] = useState(false)
  const [lastUpdated, setLastUpdated] = useState(null)
  const [now, setNow] = useState(new Date())

  // On mount: load config, then check if we already have a valid session
  useEffect(() => {
    fetchConfig()
      .then(c => {
        setCfg(c)
        if (!c.auth_required) {
          setAuthed(true)
        } else {
          fetchZombies()
            .then(() => setAuthed(true))
            .catch(err => {
              if (err.status === 401) {
                setNeedLogin(true)
                setLoading(false)
              }
            })
        }
      })
      .catch(() => setLoading(false))
  }, [])

  // Live clock tick every 1s
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])

  // Full data reload — interval depends on live mode
  useEffect(() => {
    if (!authed) return
    loadData()
    const ms = liveMode ? LIVE_POLL_MS : POLL_MS
    const id = setInterval(loadData, ms)
    return () => clearInterval(id)
  }, [authed, liveMode])

  // Snapshot polling — interval depends on live mode
  useEffect(() => {
    if (!authed) return
    const loadSnap = async () => {
      try {
        const data = await fetchSnapshot()
        setSnapshots(data)
        setLastUpdated(new Date())
      } catch {}
    }
    loadSnap()
    const ms = liveMode ? LIVE_SNAP_MS : SNAP_MS
    const id = setInterval(loadSnap, ms)
    return () => clearInterval(id)
  }, [authed, liveMode])

  async function loadData() {
    try {
      const [z, c] = await Promise.all([fetchZombies(), fetchContainers()])
      setZombies(z)
      setContainers(c)
      setLoading(false)
      setNeedLogin(false)
    } catch (err) {
      if (err.status === 401) {
        setNeedLogin(true)
        setAuthed(false)
      }
      setLoading(false)
    }
  }

  async function handleReload() {
    setReloading(true)
    try {
      const [z, c, snap] = await Promise.all([fetchZombies(), fetchContainers(), fetchSnapshot()])
      setZombies(z)
      setContainers(c)
      setSnapshots(snap)
      setLastUpdated(new Date())
    } catch (err) {
      if (err.status === 401) {
        setNeedLogin(true)
        setAuthed(false)
      }
    } finally {
      setReloading(false)
    }
  }

  if (needLogin) {
    return <Login onSuccess={() => { setNeedLogin(false); setAuthed(true) }} />
  }

  // Merge fresh snapshots into zombies list so ServiceTable always has latest metrics
  const enrichedZombies = (zombies || []).map(z => ({
    ...z,
    latest: snapshots[z.service_id] ?? z.latest,
  }))

  const zombieCount = enrichedZombies.filter(s => s.status === 'zombie').length

  return (
    <div class="min-h-screen bg-gray-950 text-gray-100">
      <header class="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-10">
        <div class="max-w-6xl mx-auto px-4 py-3 flex items-center justify-between">
          <div class="flex items-center gap-3">
            <span class="text-xl">⚓</span>
            <span class="font-bold text-white text-lg">Shipbreaker</span>
            {zombieCount > 0 && (
              <span class="bg-red-600 text-white text-xs px-2 py-0.5 rounded-full font-medium">
                {zombieCount} zombi
              </span>
            )}
          </div>
          <div class="flex items-center gap-4">
            <label class="flex items-center gap-2 cursor-pointer select-none">
              <div
                onClick={() => setLiveMode(v => !v)}
                class={`relative w-9 h-5 rounded-full transition-colors ${liveMode ? 'bg-green-600' : 'bg-gray-700'}`}
              >
                <span class={`absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform ${liveMode ? 'translate-x-4' : 'translate-x-0'}`} />
              </div>
              <span class={`text-xs font-medium ${liveMode ? 'text-green-400' : 'text-gray-500'}`}>
                {liveMode ? 'Canlı' : 'Canlı takip'}
              </span>
            </label>
            <span class="text-gray-500 text-xs" title="Son metrik güncellemesi">
              {now.toLocaleTimeString('tr-TR', { hour12: false })}
            </span>
            <span class="text-xs text-gray-600">{cfg?.tz || 'UTC'}</span>
            <button
              onClick={handleReload}
              disabled={reloading}
              title="Verileri yenile"
              class={`flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg border font-medium transition-colors ${
                reloading
                  ? 'border-gray-700 text-gray-600 cursor-not-allowed'
                  : 'border-gray-700 text-gray-400 hover:text-white hover:border-gray-500'
              }`}
            >
              <span class={reloading ? 'animate-spin inline-block' : 'inline-block'}>↻</span>
              {reloading ? 'Yükleniyor…' : 'Yenile'}
            </button>
          </div>
        </div>
      </header>

      <main class="max-w-6xl mx-auto px-4 py-6">
        <div class="flex gap-1 mb-6 bg-gray-900 border border-gray-800 rounded-xl p-1 w-fit">
          {[
            { key: 'zombies', label: 'Servisler' },
            { key: 'containers', label: 'Konteynerler' },
          ].map(({ key, label }) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              class={`px-4 py-1.5 rounded-lg text-sm font-medium transition-colors ${
                tab === key
                  ? 'bg-gray-700 text-white'
                  : 'text-gray-400 hover:text-white'
              }`}
            >
              {label}
            </button>
          ))}
        </div>

        {loading ? (
          <div class="text-center py-20 text-gray-500">Yükleniyor…</div>
        ) : tab === 'zombies' ? (
          <ServiceTable services={enrichedZombies} tz={cfg?.tz} />
        ) : (
          <ContainerList containers={containers} tz={cfg?.tz} />
        )}
      </main>
    </div>
  )
}
