export function StatusBadge({ status }) {
  const map = {
    zombie:           { label: 'Zombi',         cls: 'bg-red-900/60 text-red-300 border-red-800' },
    active:           { label: 'Aktif',          cls: 'bg-green-900/60 text-green-300 border-green-800' },
    insufficient_data:{ label: 'Yetersiz Veri',  cls: 'bg-yellow-900/60 text-yellow-300 border-yellow-800' },
    running:          { label: 'Çalışıyor',      cls: 'bg-green-900/60 text-green-300 border-green-800' },
    exited:           { label: 'Durdu',          cls: 'bg-gray-700/60 text-gray-300 border-gray-600' },
    removed:          { label: 'Silindi',        cls: 'bg-gray-800/60 text-gray-500 border-gray-700' },
  }
  const { label, cls } = map[status] ?? { label: status, cls: 'bg-gray-700 text-gray-300 border-gray-600' }
  return (
    <span class={`inline-block text-xs px-2 py-0.5 rounded-full border font-medium ${cls}`}>
      {label}
    </span>
  )
}
