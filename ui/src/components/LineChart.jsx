import { useState } from 'preact/hooks'

const PADL = 54   // left — Y axis labels
const PADB = 20   // bottom — X axis labels
const PADR = 8
const PADT = 8
const VW = 400
const VH = 120
const CW = VW - PADL - PADR
const CH = VH - PADT - PADB

function fmtXLabel(iso) {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit', hour12: false })
}

export function LineChart({ values, timestamps, yFormat = v => v.toFixed(2), color = '#3b82f6', xTickCount = 5 }) {
  const [hoverIdx, setHoverIdx] = useState(null)

  if (!values || values.length < 2) {
    return <div class="flex items-center justify-center text-gray-600 text-xs" style="height:120px">Veri yok</div>
  }

  const min = Math.min(...values)
  const max = Math.max(...values)
  const range = max - min || 1

  const px = i => PADL + (i / (values.length - 1)) * CW
  const py = v => PADT + CH - ((v - min) / range) * CH

  const pts = values.map((v, i) => ({ x: px(i), y: py(v), v }))
  const polyPts = pts.map(p => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')

  // Y ticks: 3 levels
  const yTicks = [min, (min + max) / 2, max]

  // X tick indices spread evenly
  const xStep = Math.max(1, Math.round((values.length - 1) / (xTickCount - 1)))
  const xIdxs = []
  for (let i = 0; i < values.length - 1; i += xStep) xIdxs.push(i)
  xIdxs.push(values.length - 1)

  const handleMouseMove = (e) => {
    const rect = e.currentTarget.getBoundingClientRect()
    const mouseX = (e.clientX - rect.left) / (rect.width / VW)
    let closest = 0, minDist = Infinity
    pts.forEach((p, i) => {
      const d = Math.abs(p.x - mouseX)
      if (d < minDist) { minDist = d; closest = i }
    })
    setHoverIdx(closest)
  }

  const hp = hoverIdx != null ? pts[hoverIdx] : null
  const hLabel = hoverIdx != null && timestamps ? fmtXLabel(timestamps[hoverIdx]) : null
  const hVal = hp ? yFormat(hp.v) : null

  // Tooltip sizing and clamped position
  const tipW = 120, tipH = hLabel ? 38 : 22
  const tipX = hp ? Math.min(Math.max(hp.x - tipW / 2, PADL), VW - PADR - tipW) : 0
  const tipY = hp ? (hp.y - tipH - 10 < 0 ? hp.y + 8 : hp.y - tipH - 8) : 0

  const areaPath = `M${pts[0].x.toFixed(1)},${py(min).toFixed(1)} `
    + pts.map(p => `L${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
    + ` L${pts[pts.length - 1].x.toFixed(1)},${py(min).toFixed(1)} Z`

  return (
    <svg
      viewBox={`0 0 ${VW} ${VH}`}
      class="w-full overflow-visible"
      style="cursor:crosshair"
      onMouseMove={handleMouseMove}
      onMouseLeave={() => setHoverIdx(null)}
    >
      {/* Grid lines */}
      {yTicks.map((v, i) => (
        <line key={i} x1={PADL} y1={py(v).toFixed(1)} x2={VW - PADR} y2={py(v).toFixed(1)}
          stroke="#1f2937" stroke-width="0.75" />
      ))}

      {/* Y axis labels */}
      {yTicks.map((v, i) => (
        <text key={i} x={PADL - 5} y={(py(v) + 3.5).toFixed(1)}
          text-anchor="end" font-size="9" fill="#6b7280"
          font-family="ui-monospace, monospace">
          {yFormat(v)}
        </text>
      ))}

      {/* X axis labels */}
      {xIdxs.map(i => (
        <text key={i} x={px(i).toFixed(1)} y={VH - 3}
          text-anchor="middle" font-size="9" fill="#6b7280"
          font-family="ui-monospace, monospace">
          {fmtXLabel(timestamps?.[i])}
        </text>
      ))}

      {/* Area fill */}
      <path d={areaPath} fill={color} opacity="0.08" />

      {/* Line */}
      <polyline points={polyPts} fill="none" stroke={color}
        stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />

      {/* Hover */}
      {hp && (
        <>
          <line x1={hp.x.toFixed(1)} y1={PADT} x2={hp.x.toFixed(1)} y2={(PADT + CH).toFixed(1)}
            stroke={color} stroke-width="0.75" stroke-dasharray="3,2" opacity="0.5" />
          <circle cx={hp.x.toFixed(1)} cy={hp.y.toFixed(1)} r="3.5" fill={color} />

          <rect x={tipX.toFixed(1)} y={tipY.toFixed(1)} width={tipW} height={tipH}
            rx="4" fill="#111827" stroke="#374151" stroke-width="0.75" opacity="0.95" />
          {hLabel && (
            <text x={(tipX + tipW / 2).toFixed(1)} y={(tipY + 13).toFixed(1)}
              text-anchor="middle" font-size="9" fill="#9ca3af"
              font-family="ui-monospace, monospace">{hLabel}</text>
          )}
          <text x={(tipX + tipW / 2).toFixed(1)} y={(tipY + (hLabel ? 29 : 15)).toFixed(1)}
            text-anchor="middle" font-size="11" font-weight="600" fill="#f9fafb"
            font-family="ui-monospace, monospace">{hVal}</text>
        </>
      )}
    </svg>
  )
}
