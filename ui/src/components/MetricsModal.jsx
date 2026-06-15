import { useAsync } from '../hooks.js'
import { fetchServiceMetrics } from '../api.js'
import { LineChart } from './LineChart.jsx'

function fmtBytes(b) {
  if (b == null) return '—'
  if (b < 1024) return `${b} B`
  if (b < 1024 ** 2) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(1)} MB`
  return `${(b / 1024 ** 3).toFixed(2)} GB`
}

function fmtDate(iso, tz) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('tr-TR', { timeZone: tz || 'UTC', hour12: false })
}

export function MetricsModal({ service, tz, onClose }) {
  const { data: points, loading, error } = useAsync(
    () => fetchServiceMetrics(service.service_id),
    [service.service_id]
  )

  const cpuValues  = points?.map(p => p.cpu_avg_pct) ?? []
  const memValues  = points?.map(p => p.memory_ws_avg_bytes) ?? []
  const netValues  = points?.map(p => (p.net_rx_bytes + p.net_tx_bytes) / 1024 / 1024) ?? []
  const timestamps = points?.map(p => p.bucket) ?? []

  const charts = [
    {
      title: 'CPU (çekirdek-başına %)',
      values: cpuValues,
      color: '#3b82f6',
      yFormat: v => `${v.toFixed(2)}%`,
    },
    {
      title: 'RAM (bellek)',
      values: memValues,
      color: '#a855f7',
      yFormat: fmtBytes,
    },
    {
      title: 'Ağ (MB/saat)',
      values: netValues,
      color: '#10b981',
      yFormat: v => `${v.toFixed(2)} MB`,
    },
  ]

  return (
    <div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4" onClick={onClose}>
      <div
        class="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-2xl max-h-[80vh] overflow-y-auto shadow-2xl"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div class="flex items-start justify-between p-5 border-b border-gray-800">
          <div>
            <p class="text-xs text-gray-500 font-mono mb-1">{service.service_key}</p>
            <h2 class="text-white font-semibold text-lg">{service.name || service.image}</h2>
            <p class="text-gray-400 text-sm">{service.image}</p>
          </div>
          <button onClick={onClose} class="text-gray-500 hover:text-white text-xl leading-none mt-1">✕</button>
        </div>

        <div class="p-5">
          {loading && <p class="text-gray-400 text-sm">Yükleniyor…</p>}
          {error && <p class="text-red-400 text-sm">Hata: {error.message}</p>}

          {points && (
            <>
              {/* Charts */}
              <div class="flex flex-col gap-4 mb-6">
                {charts.map(({ title, values, color, yFormat }) => (
                  <div key={title} class="bg-gray-800 rounded-lg p-3">
                    <p class="text-xs text-gray-400 mb-2">{title}</p>
                    <LineChart
                      values={values}
                      timestamps={timestamps}
                      color={color}
                      yFormat={yFormat}
                      xTickCount={5}
                    />
                  </div>
                ))}
              </div>

              {/* Data table */}
              {points.length === 0 ? (
                <p class="text-gray-500 text-sm text-center py-4">Henüz metrik yok.</p>
              ) : (
                <div class="overflow-x-auto">
                  <table class="w-full text-sm text-left">
                    <thead>
                      <tr class="text-gray-500 text-xs uppercase border-b border-gray-800">
                        <th class="py-2 pr-4 font-medium">Saat</th>
                        <th class="py-2 pr-4 font-medium">CPU avg</th>
                        <th class="py-2 pr-4 font-medium">CPU max</th>
                        <th class="py-2 pr-4 font-medium">Bellek</th>
                        <th class="py-2 pr-4 font-medium">Ağ rx+tx</th>
                        <th class="py-2 font-medium">Disk r+w</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...points].reverse().slice(0, 72).map(p => (
                        <tr key={p.bucket} class="border-b border-gray-800/50 hover:bg-gray-800/30">
                          <td class="py-1.5 pr-4 text-gray-400 font-mono text-xs whitespace-nowrap">
                            {fmtDate(p.bucket, tz)}
                          </td>
                          <td class="py-1.5 pr-4 text-white">{p.cpu_avg_pct.toFixed(2)}%</td>
                          <td class="py-1.5 pr-4 text-gray-300">{p.cpu_max_pct.toFixed(2)}%</td>
                          <td class="py-1.5 pr-4 text-gray-300">{fmtBytes(p.memory_ws_avg_bytes)}</td>
                          <td class="py-1.5 pr-4 text-gray-300">{fmtBytes(p.net_rx_bytes + p.net_tx_bytes)}</td>
                          <td class="py-1.5 text-gray-300">{fmtBytes(p.blk_read_bytes + p.blk_write_bytes)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
