const actor = 'demo.user@acme.example'

export async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(path)
  if (!response.ok) throw new Error(await errorText(response))
  return response.json() as Promise<T>
}

export async function postJSON<T>(path: string, body: unknown = {}): Promise<T> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-KubeSqueeze-Actor': actor },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw new Error(await errorText(response))
  return response.json() as Promise<T>
}

export async function deleteJSON(path: string): Promise<void> {
  const response = await fetch(path, {
    method: 'DELETE',
    headers: { 'X-KubeSqueeze-Actor': actor },
  })
  if (!response.ok) throw new Error(await errorText(response))
}

type DraftStreamEvent<T> =
  | { type: 'status'; status: string }
  | { type: 'delta'; delta: string }
  | { type: 'complete'; draft: T }
  | { type: 'error'; error: string }

export async function streamPolicyDraft<T>(requirements: string, onDelta: (delta: string) => void): Promise<T> {
  const response = await fetch('/api/v1/policies/draft/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-KubeSqueeze-Actor': actor },
    body: JSON.stringify({ requirements }),
  })
  if (!response.ok) throw new Error(await errorText(response))
  if (!response.body) throw new Error('Policy draft streaming is unavailable')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let completed: T | undefined

  const processLine = (line: string) => {
    if (!line.trim()) return
    const event = JSON.parse(line) as DraftStreamEvent<T>
    if (event.type === 'delta') onDelta(event.delta)
    if (event.type === 'complete') completed = event.draft
    if (event.type === 'error') throw new Error(event.error)
  }

  while (true) {
    const { value, done } = await reader.read()
    buffer += decoder.decode(value, { stream: !done })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) processLine(line)
    if (done) break
  }
  processLine(buffer)
  if (!completed) throw new Error('Policy stream ended before a validated draft was saved')
  return completed
}

async function errorText(response: Response) {
  try {
    const value = (await response.json()) as { error?: string }
    return value.error ?? response.statusText
  } catch {
    return response.statusText
  }
}
