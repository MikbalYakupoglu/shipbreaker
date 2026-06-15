import { useState, useEffect, useCallback } from 'preact/hooks'

// Generic data fetching hook
export function useAsync(fn, deps = []) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  const run = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const result = await fn()
      setData(result)
    } catch (err) {
      setError(err)
    } finally {
      setLoading(false)
    }
  }, deps)

  useEffect(() => { run() }, [run])

  return { data, loading, error, refetch: run }
}

// Poll on an interval (ms)
export function usePolling(fn, intervalMs, deps = []) {
  const result = useAsync(fn, deps)

  useEffect(() => {
    const id = setInterval(result.refetch, intervalMs)
    return () => clearInterval(id)
  }, [result.refetch, intervalMs])

  return result
}
