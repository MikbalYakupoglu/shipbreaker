// Base fetch wrapper — 401 → triggers re-login via thrown error
async function apiFetch(path, opts = {}) {
  const res = await fetch(path, { credentials: 'same-origin', ...opts })
  if (res.status === 401) {
    const err = new Error('unauthorized')
    err.status = 401
    throw err
  }
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}`)
  }
  if (res.headers.get('content-type')?.includes('application/json')) {
    return res.json()
  }
  return res.text()
}

export async function fetchConfig() {
  return apiFetch('/api/config')
}

export async function login(user, password) {
  const res = await fetch('/api/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user, password }),
  })
  if (!res.ok) throw new Error('invalid credentials')
  return res.json()
}

export async function fetchZombies() {
  return apiFetch('/api/zombies')
}

export async function fetchSnapshot() {
  return apiFetch('/api/snapshot')
}

export async function fetchContainers() {
  return apiFetch('/api/containers')
}

export async function fetchServiceMetrics(serviceID) {
  return apiFetch(`/api/services/${serviceID}/metrics`)
}

export async function fetchContainer(id) {
  return apiFetch(`/api/containers/${id}`)
}

export async function fetchContainerMetrics(id) {
  return apiFetch(`/api/containers/${id}/metrics`)
}
