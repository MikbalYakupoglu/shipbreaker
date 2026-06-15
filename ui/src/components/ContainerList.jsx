import { useState, useEffect } from 'preact/hooks'
import { StatusBadge } from './Badge.jsx'
import { ContainerModal } from './ContainerModal.jsx'

function fmtDate(iso, tz) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('tr-TR', { timeZone: tz || 'UTC', hour12: false })
}

function uptime(isoCreated, now) {
  if (!isoCreated) return ''
  const secs = Math.floor((now - new Date(isoCreated)) / 1000)
  if (secs < 0) return '—'
  if (secs < 60) return `${secs}sn`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}dk`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}sa`
  return `${Math.floor(hrs / 24)}gün`
}

export function ContainerList({ containers, tz }) {
  const [selected, setSelected] = useState(null)
  const [now, setNow] = useState(Date.now())

  // Tick every second so uptime counters update live
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  if (!containers || containers.length === 0) {
    return <p class="text-gray-500 text-sm text-center py-8">Konteyner bulunamadı.</p>
  }

  return (
    <>
      <div class="overflow-x-auto rounded-xl border border-gray-800">
        <table class="w-full text-sm text-left">
          <thead>
            <tr class="text-gray-500 text-xs uppercase bg-gray-900/50 border-b border-gray-800">
              <th class="py-3 px-4 font-medium">Durum</th>
              <th class="py-3 px-4 font-medium">Ad</th>
              <th class="py-3 px-4 font-medium">İmaj</th>
              <th class="py-3 px-4 font-medium">Uptime</th>
              <th class="py-3 px-4 font-medium">Son Görülme</th>
              <th class="py-3 px-4 font-medium">ID</th>
            </tr>
          </thead>
          <tbody>
            {containers.map(c => (
              <tr
                key={c.id}
                class="border-b border-gray-800/50 hover:bg-gray-800/30 cursor-pointer transition-colors"
                onClick={() => setSelected(c)}
              >
                <td class="py-2.5 px-4"><StatusBadge status={c.status} /></td>
                <td class="py-2.5 px-4 text-white font-medium">{c.name}</td>
                <td class="py-2.5 px-4 text-gray-400 font-mono text-xs">{c.image}</td>
                <td class="py-2.5 px-4 text-gray-400 text-xs">{uptime(c.created_at, now)}</td>
                <td class="py-2.5 px-4 text-gray-400 text-xs">{fmtDate(c.last_seen_at, tz)}</td>
                <td class="py-2.5 px-4 text-gray-600 font-mono text-xs">{c.id.slice(0, 12)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {selected && (
        <ContainerModal
          container={selected}
          tz={tz}
          onClose={() => setSelected(null)}
        />
      )}
    </>
  )
}
