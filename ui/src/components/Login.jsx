import { useState } from 'preact/hooks'
import { login } from '../api.js'

export function Login({ onSuccess }) {
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(false)

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      await login(user, password)
      onSuccess()
    } catch {
      setError('Kullanıcı adı veya şifre hatalı.')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div class="min-h-screen bg-gray-950 flex items-center justify-center">
      <div class="bg-gray-900 border border-gray-800 rounded-xl p-8 w-full max-w-sm shadow-xl">
        <div class="flex items-center gap-3 mb-8">
          <span class="text-2xl">⚓</span>
          <h1 class="text-xl font-bold text-white">Shipbreaker</h1>
        </div>

        <form onSubmit={handleSubmit} class="flex flex-col gap-4">
          <div class="flex flex-col gap-1">
            <label class="text-xs text-gray-400 uppercase tracking-wider">Kullanıcı</label>
            <input
              type="text"
              value={user}
              onInput={e => setUser(e.target.value)}
              class="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-white text-sm focus:outline-none focus:border-blue-500"
              required
              autoFocus
            />
          </div>

          <div class="flex flex-col gap-1">
            <label class="text-xs text-gray-400 uppercase tracking-wider">Şifre</label>
            <input
              type="password"
              value={password}
              onInput={e => setPassword(e.target.value)}
              class="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-white text-sm focus:outline-none focus:border-blue-500"
              required
            />
          </div>

          {error && (
            <p class="text-red-400 text-sm">{error}</p>
          )}

          <button
            type="submit"
            disabled={loading}
            class="mt-2 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white rounded-lg px-4 py-2 text-sm font-medium transition-colors"
          >
            {loading ? 'Giriş yapılıyor…' : 'Giriş Yap'}
          </button>
        </form>
      </div>
    </div>
  )
}
