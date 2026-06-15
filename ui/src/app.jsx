import { useState, useEffect } from 'preact/hooks'
import { fetchConfig, fetchZombies, fetchContainers, fetchSnapshot } from './api.js'
import { Login } from './components/Login.jsx'
import { ServiceTable } from './components/ServiceTable.jsx'
import { ContainerList } from './components/ContainerList.jsx'

const POLL_MS = 30_000  // full analysis + containers
const SNAP_MS = 5_000   // lightweight latest-metrics snapshot

export function App() {
  const [cfg, setCfg] = useState(null)
  const [authed, setAuthed] = useState(false)
  const [needLogin, setNeedLogin] = useState(false)
  const [tab, setTab] = useState('zombies')
  const [zombies, setZombies] = useState(null)
  const [containers, setContainers] = useState(null)
  const [snapshots, setSnapshots] = useState({})
  const [loading, setLoading] = useState(true)
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

  // Full data reload every 30s (analysis + containers)
  useEffect(() => {
    if (!authed) return
    loadData()
    const id = setInterval(loadData, POLL_MS)
    return () => clearInterval(id)
  }, [authed])

  // Fast snapshot polling every 5s (just latest metrics, no analyzer)
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
    const id = setInterval(loadSnap, SNAP_MS)
    return () => clearInterval(id)
  }, [authed])

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
            <span class="text-gray-500 text-xs" title="Son metrik güncellemesi">
              {now.toLocaleTimeString('tr-TR', { hour12: false })}
            </span>
            <span class="text-xs text-gray-600">{cfg?.tz || 'UTC'}</span>
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
