import { useState, useEffect, useRef } from 'preact/hooks'
import { fetchConfig, fetchZombies, fetchContainers, fetchSnapshot, setLiveMode as apiSetLiveMode, triggerRefresh } from './api.js'
import { Login } from './components/Login.jsx'
import { ServiceTable } from './components/ServiceTable.jsx'
import { ContainerList } from './components/ContainerList.jsx'


export function App() {
  const [cfg, setCfg] = useState(null)
  const [authed, setAuthed] = useState(false)
  const [needLogin, setNeedLogin] = useState(false)
  const [tab, setTab] = useState('zombies')
  const [zombies, setZombies] = useState(null)
  const [containers, setContainers] = useState(null)
  const [snapshots, setSnapshots] = useState({})
  const [loading, setLoading] = useState(true)

  function switchTab(next) {
    if (next === tab) return
    if (next === 'containers' && containers === null) setLoading(true)
    if (next === 'zombies' && zombies === null) setLoading(true)
    setTab(next)
  }
  const zombieReqRef = useRef(false)
  const containerReqRef = useRef(false)
  const [reloading, setReloading] = useState(false)
  const [reloadCooldown, setReloadCooldown] = useState(false)
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

  const pollMs = liveMode
    ? (cfg?.live_interval_sec ?? 5) * 1000
    : (cfg?.sample_interval_sec ?? 60) * 1000

  // Zombies + snapshot polling — only when on services tab
  useEffect(() => {
    if (!authed || tab !== 'zombies') return
    loadZombies()
    loadSnapshot()
    const zi = setInterval(loadZombies, pollMs)
    const si = setInterval(loadSnapshot, pollMs)
    return () => { clearInterval(zi); clearInterval(si) }
  }, [authed, tab, pollMs])

  // Container polling — only when on containers tab
  useEffect(() => {
    if (!authed || tab !== 'containers') return
    loadContainers()
    const id = setInterval(loadContainers, pollMs)
    return () => clearInterval(id)
  }, [authed, tab, pollMs])

  async function loadZombies() {
    if (zombieReqRef.current) return
    zombieReqRef.current = true
    try {
      const z = await fetchZombies()
      setZombies(z)
      setLoading(false)
      setNeedLogin(false)
    } catch (err) {
      if (err.status === 401) { setNeedLogin(true); setAuthed(false) }
      setLoading(false)
    } finally {
      zombieReqRef.current = false
    }
  }

  async function loadContainers() {
    if (containerReqRef.current) return
    containerReqRef.current = true
    try {
      const c = await fetchContainers()
      setContainers(c)
      setLoading(false)
      setNeedLogin(false)
    } catch (err) {
      if (err.status === 401) { setNeedLogin(true); setAuthed(false) }
      setLoading(false)
    } finally {
      containerReqRef.current = false
    }
  }

  async function loadSnapshot() {
    try {
      const data = await fetchSnapshot()
      setSnapshots(data)
      setLastUpdated(new Date())
    } catch {}
  }

  async function handleReload() {
    setReloading(true)
    setReloadCooldown(true)
    setTimeout(() => setReloadCooldown(false), 3000)
    try {
      await triggerRefresh()
      if (tab === 'zombies') {
        const [z, snap] = await Promise.all([fetchZombies(), fetchSnapshot()])
        setZombies(z)
        setSnapshots(snap)
        setLastUpdated(new Date())
      } else {
        const c = await fetchContainers()
        setContainers(c)
      }
    } catch (err) {
      if (err.status === 401) { setNeedLogin(true); setAuthed(false) }
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
                onClick={() => {
                  const next = !liveMode
                  setLiveMode(next)
                  apiSetLiveMode(next).catch(() => {})
                }}
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
              disabled={reloading || reloadCooldown}
              title="Verileri yenile"
              class={`flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg border font-medium transition-colors ${
                reloading || reloadCooldown
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
              onClick={() => switchTab(key)}
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
