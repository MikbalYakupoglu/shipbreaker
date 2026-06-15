import { useState, useEffect } from 'preact/hooks'
import { usePolling } from '../hooks.js'
import { fetchContainer, fetchContainerMetrics } from '../api.js'
import { StatusBadge } from './Badge.jsx'
import { LineChart } from './LineChart.jsx'

function fmtBytes(b) {
  if (b == null || b < 0) return '—'
  if (b < 1024) return `${b} B`
  if (b < 1024 ** 2) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(1)} MB`
  return `${(b / 1024 ** 3).toFixed(2)} GB`
}

function fmtDate(iso, tz) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('tr-TR', { timeZone: tz || 'UTC', hour12: false })
}

function uptime(isoCreated, now) {
  if (!isoCreated) return '—'
  const secs = Math.floor((now - new Date(isoCreated)) / 1000)
  if (secs < 0) return '—'
  if (secs < 60) return `${secs} sn`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins} dk`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs} sa ${mins % 60} dk`
  const days = Math.floor(hrs / 24)
  return `${days} gün ${hrs % 24} sa`
}

function MetricCard({ label, value, sub }) {
  return (
    <div class="bg-gray-800 rounded-lg p-3 flex flex-col gap-0.5">
      <span class="text-xs text-gray-500 uppercase tracking-wider">{label}</span>
      <span class="text-white font-semibold text-base">{value}</span>
      {sub && <span class="text-gray-500 text-xs">{sub}</span>}
    </div>
  )
}

export function ContainerModal({ container, tz, onClose }) {
  const [now, setNow] = useState(Date.now())

  // Tick every second for live uptime counter
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  // Poll container detail (latest metrics) every 10s
  const { data: detail, loading: detailLoading } = usePolling(
    () => fetchContainer(container.id),
    10_000,
    [container.id]
  )

  // Poll sparkline data every 30s
  const { data: pts } = usePolling(
    () => fetchContainerMetrics(container.id),
    30_000,
    [container.id]
  )

  const cpuValues = pts?.map(p => p.cpu_pct) ?? []
  const memValues = pts?.map(p => p.mem_bytes) ?? []
  const timestamps = pts?.map(p => p.t) ?? []

  const netValues = pts?.map((p, i) => {
    if (i === 0) return 0
    const prev = pts[i - 1]
    const rx = Math.max(0, p.net_rx_bytes - prev.net_rx_bytes)
    const tx = Math.max(0, p.net_tx_bytes - prev.net_tx_bytes)
    return (rx + tx) / 1024
  }) ?? []

  const d = detail || container

  return (
    <div
      class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4"
      onClick={onClose}
    >
      <div
        class="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-2xl max-h-[85vh] overflow-y-auto shadow-2xl"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div class="flex items-start justify-between p-5 border-b border-gray-800">
          <div class="flex flex-col gap-1">
            <div class="flex items-center gap-2">
              <StatusBadge status={container.status} />
              <span class="text-white font-bold text-lg">{container.name}</span>
            </div>
            <span class="text-gray-400 text-sm font-mono">{container.image}</span>
            <span class="text-gray-600 text-xs font-mono">{container.id}</span>
          </div>
          <button
            onClick={onClose}
            class="text-gray-500 hover:text-white text-xl leading-none mt-1 ml-4 flex-shrink-0"
          >✕</button>
        </div>

        <div class="p-5 flex flex-col gap-5">
          {detailLoading && !d ? (
            <p class="text-gray-500 text-sm">Yükleniyor…</p>
          ) : (
            <>
              {/* Uptime + meta */}
              <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
                <MetricCard
                  label="Uptime"
                  value={uptime(d.created_at, now)}
                  sub={`Başlangıç: ${fmtDate(d.created_at, tz)}`}
                />
                <MetricCard
                  label="İlk Gözlem"
                  value={fmtDate(d.first_seen_at, tz)}
                />
                <MetricCard
                  label="Son Gözlem"
                  value={fmtDate(d.last_seen_at, tz)}
                />
              </div>

              {/* Live metrics */}
              {d.latest_cpu_pct != null ? (
                <div>
                  <h3 class="text-xs text-gray-500 uppercase tracking-wider mb-2">Anlık Metrikler</h3>
                  <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
                    <MetricCard
                      label="CPU"
                      value={`${d.latest_cpu_pct.toFixed(2)}%`}
                      sub="çekirdek-başına"
                    />
                    <MetricCard
                      label="Bellek (WS)"
                      value={fmtBytes(d.latest_mem_bytes)}
                    />
                    <MetricCard
                      label="Ağ Rx"
                      value={fmtBytes(d.latest_net_rx_bytes)}
                      sub="kümülatif"
                    />
                    <MetricCard
                      label="Ağ Tx"
                      value={fmtBytes(d.latest_net_tx_bytes)}
                      sub="kümülatif"
                    />
                    <MetricCard
                      label="Disk Okuma"
                      value={fmtBytes(d.latest_blk_read_bytes)}
                      sub="kümülatif"
                    />
                    <MetricCard
                      label="Disk Yazma"
                      value={fmtBytes(d.latest_blk_write_bytes)}
                      sub="kümülatif"
                    />
                  </div>
                </div>
              ) : (
                <p class="text-yellow-600 text-sm bg-yellow-950/30 border border-yellow-900/40 rounded-lg px-3 py-2">
                  Henüz metrik toplanmamış. Watcher ilk örneklemeyi yapmayı bekliyor.
                </p>
              )}

              {/* Charts — last ~hour */}
              {cpuValues.length >= 2 && (
                <div>
                  <h3 class="text-xs text-gray-500 uppercase tracking-wider mb-2">
                    Son {cpuValues.length} dakika
                  </h3>
                  <div class="flex flex-col gap-3">
                    <div class="bg-gray-800 rounded-lg p-3">
                      <p class="text-xs text-gray-400 mb-2">CPU %</p>
                      <LineChart values={cpuValues} timestamps={timestamps} color="#3b82f6" yFormat={v => `${v.toFixed(2)}%`} xTickCount={5} />
                    </div>
                    <div class="bg-gray-800 rounded-lg p-3">
                      <p class="text-xs text-gray-400 mb-2">RAM (bellek)</p>
                      <LineChart values={memValues} timestamps={timestamps} color="#a855f7" yFormat={fmtBytes} xTickCount={5} />
                    </div>
                    <div class="bg-gray-800 rounded-lg p-3">
                      <p class="text-xs text-gray-400 mb-2">Ağ delta (KB/dk)</p>
                      <LineChart values={netValues} timestamps={timestamps} color="#10b981" yFormat={v => `${v.toFixed(1)} KB`} xTickCount={5} />
                    </div>
                  </div>
                </div>
              )}

              {/* Service link */}
              <div class="border-t border-gray-800 pt-4">
                <p class="text-xs text-gray-500 uppercase tracking-wider mb-1">Servis</p>
                <p class="text-gray-300 font-mono text-sm">{d.service_key}</p>
                <p class="text-gray-600 text-xs mt-0.5">{d.service_id}</p>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
