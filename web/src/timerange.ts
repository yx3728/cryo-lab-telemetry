// Time-range presets for the dashboard. A preset is resolved to concrete
// from/to Dates at fetch time, so "last 1h" always means relative to now.

export interface RangePreset {
  label: string;
  ms: number;
}

export const PRESETS: RangePreset[] = [
  { label: "15m", ms: 15 * 60_000 },
  { label: "1h", ms: 60 * 60_000 },
  { label: "3h", ms: 3 * 60 * 60_000 },
  { label: "12h", ms: 12 * 60 * 60_000 },
  { label: "24h", ms: 24 * 60 * 60_000 },
  { label: "7d", ms: 7 * 24 * 60 * 60_000 },
];

export function rangeFor(preset: RangePreset, now: number = Date.now()): { from: Date; to: Date } {
  return { from: new Date(now - preset.ms), to: new Date(now) };
}

// formatTick picks a sensible axis label for the given total span.
export function formatTick(tsMs: number, spanMs: number): string {
  const d = new Date(tsMs);
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  if (spanMs <= 60 * 60_000) {
    const ss = String(d.getSeconds()).padStart(2, "0");
    return `${hh}:${mm}:${ss}`;
  }
  if (spanMs <= 24 * 60 * 60_000) return `${hh}:${mm}`;
  const mo = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${mo}/${day} ${hh}:${mm}`;
}
