import { useState } from 'preact/hooks'

export function Sparkline({ values, labels, unit = '', width = 120, height = 32, color = '#3b82f6' }) {
  const [hoverIdx, setHoverIdx] = useState(null)

  if (!values || values.length < 2) {
    return <svg width={width} height={height} />
  }

  const min = Math.min(...values)
  const max = Math.max(...values)
  const range = max - min || 1

  const pts = values.map((v, i) => ({
    x: (i / (values.length - 1)) * width,
    y: height - ((v - min) / range) * (height - 4) - 2,
    v,
  }))

  const ptStr = pts.map(p => `${p.x},${p.y}`).join(' ')

  const handleMouseMove = (e) => {
    const rect = e.currentTarget.getBoundingClientRect()
    const mouseX = (e.clientX - rect.left) * (width / rect.width)
    let closest = 0
    let minDist = Infinity
    pts.forEach((p, i) => {
      const d = Math.abs(p.x - mouseX)
      if (d < minDist) { minDist = d; closest = i }
    })
    setHoverIdx(closest)
  }

  const hp = hoverIdx != null ? pts[hoverIdx] : null
  const label = hoverIdx != null && labels ? labels[hoverIdx] : null
  const valStr = hp != null ? `${hp.v.toFixed(2)}${unit}` : ''

  // tooltip dimensions & clamped position
  const tipW = 130
  const tipH = label ? 38 : 22
  const tipX = hp ? Math.min(Math.max(hp.x - tipW / 2, -8), width - tipW + 8) : 0
  const tipY = hp ? (hp.y - tipH - 8 < -10 ? hp.y + 10 : hp.y - tipH - 8) : 0

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      class="overflow-visible w-full"
      onMouseMove={handleMouseMove}
      onMouseLeave={() => setHoverIdx(null)}
      style="cursor:crosshair"
    >
      <polyline
        points={ptStr}
        fill="none"
        stroke={color}
        stroke-width="1.5"
        stroke-linejoin="round"
        stroke-linecap="round"
      />

      {hp && (
        <>
          {/* crosshair */}
          <line
            x1={hp.x} y1={0} x2={hp.x} y2={height}
            stroke={color} stroke-width="0.75" stroke-dasharray="3,3" opacity="0.5"
          />
          {/* dot */}
          <circle cx={hp.x} cy={hp.y} r="3.5" fill={color} />

          {/* tooltip box */}
          <rect
            x={tipX} y={tipY}
            width={tipW} height={tipH}
            rx="4" ry="4"
            fill="#111827" stroke="#374151" stroke-width="0.75"
            opacity="0.95"
          />
          {label && (
            <text
              x={tipX + tipW / 2} y={tipY + 13}
              text-anchor="middle"
              font-size="9"
              fill="#9ca3af"
              font-family="ui-monospace, monospace"
            >{label}</text>
          )}
          <text
            x={tipX + tipW / 2} y={tipY + (label ? 28 : 14)}
            text-anchor="middle"
            font-size="11"
            fill="#f9fafb"
            font-weight="600"
            font-family="ui-monospace, monospace"
          >{valStr}</text>
        </>
      )}
    </svg>
  )
}
