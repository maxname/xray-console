import { useEffect, useState } from 'react'

function unixNow(): number {
  return Math.floor(Date.now() / 1000)
}

/**
 * The current time in Unix seconds, as state: reading the clock during render
 * would make "down for 14 minutes" depend on when React happened to render.
 * It ticks often enough for durations shown in minutes.
 */
export function useNow(intervalMs = 20_000): number {
  const [now, setNow] = useState(unixNow)
  useEffect(() => {
    const id = setInterval(() => setNow(unixNow()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}
