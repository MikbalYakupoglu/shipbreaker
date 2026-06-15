import { useState } from 'preact/hooks'
import { StatusBadge } from './Badge.jsx'
import { MetricsModal } from './MetricsModal.jsx'

function fmtBytes(b) {
  if (b == null || b < 0) return '—'
  if (b < 1024) return `${b} B`
  if (b < 1024 ** 2) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(1)} MB`
  return `${(b / 1024 ** 3).toFixed(2)} GB`
}

function fmtBytesPerDay(bpd) {
  if (bpd == null || bpd <= 0) return '—'
  return fmtBytes(bpd) + '/gün'
}

function fmtCPU(pct) {
  if (pct == null) return '—'
  return `${pct.toFixed(2)}%`
}

// Dim label for "Yetersiz Veri" rows where we show live snapshot values
function LiveTag() {
  return (
    <span class="text-xs text-gray-600 ml-1" title="anlık değer">(anlık)</span>
  )
}

export function ServiceTable({ services, tz }) {
  const [selected, setSelected] = useState(null)
  const [filter, setFilter] = useState('all')

  const filtered = (services || []).filter(s =>
    filter === 'all' ? true : s.status === filter
  )

  const zombieCount = (services || []).filter(s => s.status === 'zombie').length
  const activeCount = (services || []).filter(s => s.status === 'active').length
  const insuffCount = (services || []).filter(s => s.status === 'insufficient_data').length

  return (
    <div>
      {/* Filter bar */}
      <div class="flex items-center gap-2 mb-4 flex-wrap">
        {[
          { key: 'all',              label: `Tümü (${(services || []).length})` },
          { key: 'zombie',           label: `Zombi (${zombieCount})` },
          { key: 'active',           label: `Aktif (${activeCount})` },
          { key: 'insufficient_data',label: `Yetersiz Veri (${insuffCount})` },
        ].map(({ key, label }) => (
          <button
            key={key}
            onClick={() => setFilter(key)}
            class={`text-xs px-3 py-1.5 rounded-full border font-medium transition-colors ${
              filter === key
                ? 'bg-blue-600 border-blue-500 text-white'
                : 'border-gray-700 text-gray-400 hover:text-white hover:border-gray-500'
            }`}
          >
            {label}
          </button>
        ))}
      </div>

      {filtered.length === 0 ? (
        <div class="text-center py-12 text-gray-500">
          {filter === 'zombie'
            ? 'Hiç zombi servis bulunamadı. İyi haber!'
            : 'Servis yok.'}
        </div>
      ) : (
        <div class="overflow-x-auto rounded-xl border border-gray-800">
          <table class="w-full text-sm text-left">
            <thead>
              <tr class="text-gray-500 text-xs uppercase bg-gray-900/50 border-b border-gray-800">
                <th class="py-3 px-4 font-medium">Durum</th>
                <th class="py-3 px-4 font-medium">Servis</th>
                <th class="py-3 px-4 font-medium">CPU</th>
                <th class="py-3 px-4 font-medium">RAM</th>
                <th class="py-3 px-4 font-medium">Ağ (rx+tx)</th>
                <th class="py-3 px-4 font-medium">Disk (r+w)</th>
                <th class="py-3 px-4 font-medium">Örnekler</th>
                <th class="py-3 px-4 font-medium"></th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(svc => {
                const hasAvg = svc.status !== 'insufficient_data'
                const L = svc.latest || {}
                const hasLive = L.latest_cpu_pct != null

                return (
                  <tr
                    key={svc.service_id}
                    class="border-b border-gray-800/50 hover:bg-gray-800/20 transition-colors"
                  >
                    <td class="py-3 px-4">
                      <StatusBadge status={svc.status} />
                    </td>

                    <td class="py-3 px-4">
                      <div class="font-medium text-white">{svc.name || svc.image}</div>
                      <div class="text-gray-500 text-xs font-mono truncate max-w-xs">
                        {svc.service_key}
                      </div>
                    </td>

                    {/* CPU — avg if evaluated, live snapshot if insufficient */}
                    <td class="py-3 px-4">
                      {hasAvg ? (
                        <div>
                          <span class="text-white">{fmtCPU(svc.cpu_avg_pct)}</span>
                          <span class="text-gray-500 text-xs ml-1">ort</span>
                        </div>
                      ) : hasLive ? (
                        <div>
                          <span class="text-gray-300">{fmtCPU(L.latest_cpu_pct)}</span>
                          <LiveTag />
                        </div>
                      ) : (
                        <span class="text-gray-600">—</span>
                      )}
                    </td>

                    {/* RAM — always from live snapshot */}
                    <td class="py-3 px-4">
                      {hasLive ? (
                        <div>
                          <span class="text-gray-300">{fmtBytes(L.latest_mem_bytes)}</span>
                          <LiveTag />
                        </div>
                      ) : (
                        <span class="text-gray-600">—</span>
                      )}
                    </td>

                    {/* Network */}
                    <td class="py-3 px-4">
                      {hasAvg ? (
                        <div>
                          <span class="text-white">{fmtBytesPerDay(svc.net_bytes_per_day)}</span>
                        </div>
                      ) : hasLive ? (
                        <div>
                          <span class="text-gray-300">
                            {fmtBytes((L.latest_net_rx_bytes || 0) + (L.latest_net_tx_bytes || 0))}
                          </span>
                          <LiveTag />
                        </div>
                      ) : (
                        <span class="text-gray-600">—</span>
                      )}
                    </td>

                    {/* Disk */}
                    <td class="py-3 px-4">
                      {hasAvg ? (
                        <span class="text-white">{fmtBytesPerDay(svc.disk_bytes_per_day)}</span>
                      ) : hasLive ? (
                        <div>
                          <span class="text-gray-300">
                            {fmtBytes((L.latest_blk_read_bytes || 0) + (L.latest_blk_write_bytes || 0))}
                          </span>
                          <LiveTag />
                        </div>
                      ) : (
                        <span class="text-gray-600">—</span>
                      )}
                    </td>

                    <td class="py-3 px-4 text-gray-400 text-xs">
                      {svc.sample_count > 0 ? (
                        <span title={`${svc.sample_count} / 84 saat`}>
                          {svc.sample_count}
                          <span class="text-gray-600"> / 84</span>
                        </span>
                      ) : '0'}
                    </td>

                    <td class="py-3 px-4">
                      <button
                        onClick={() => setSelected(svc)}
                        class="text-xs text-blue-400 hover:text-blue-300 underline-offset-2 hover:underline whitespace-nowrap"
                      >
                        Geçmiş
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Info bar for insufficient_data explanation */}
      {insuffCount > 0 && filter !== 'zombie' && filter !== 'active' && (
        <p class="text-gray-600 text-xs mt-3">
          Zombi/Aktif kararı için 84 saatlik kova verisi gerekli (~3.5 gün). Anlık değerler mevcut metriklerden alınmaktadır.
        </p>
      )}

      {selected && (
        <MetricsModal service={selected} tz={tz} onClose={() => setSelected(null)} />
      )}
    </div>
  )
}
