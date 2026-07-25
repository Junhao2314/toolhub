import { useCallback, useEffect, useState } from 'react'

export function useData<T>(loader: () => Promise<T>, dependencies: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const reload = useCallback(() => {
    setLoading(true)
    setError('')
    loader().then(setData).catch((reason: Error) => setError(reason.message)).finally(() => setLoading(false))
  }, dependencies)
  useEffect(reload, [reload])
  return { data, error, loading, reload, setData }
}
