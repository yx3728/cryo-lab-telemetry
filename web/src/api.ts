// Thin typed client over the Go API. All paths are relative so the same build
// works behind Vite's dev proxy and behind Caddy in production.

export interface Channel {
  source: string;
  metric: string;
  last_ts: string;
  last_value: number;
}

export interface SeriesPoint {
  ts: string;
  value: number;
}

export interface SeriesResponse {
  source: string;
  metric: string;
  step: string;
  from: string;
  to: string;
  points: SeriesPoint[];
}

export interface Threshold {
  metric: string;
  min: number | null;
  max: number | null;
  enabled: boolean;
}

export interface ConfigPayload {
  sampling_interval_seconds: number;
  alert_max_emails_per_day: number;
  thresholds: Threshold[];
}

const TOKEN_KEY = "labmon_jwt";

export const getToken = (): string | null => localStorage.getItem(TOKEN_KEY);
export const setToken = (t: string): void => localStorage.setItem(TOKEN_KEY, t);
export const clearToken = (): void => localStorage.removeItem(TOKEN_KEY);

async function getJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${url} -> ${resp.status}`);
  return resp.json() as Promise<T>;
}

export async function getChannels(): Promise<Channel[]> {
  const data = await getJSON<{ channels: Channel[] }>("/api/channels");
  return data.channels;
}

export function seriesUrl(source: string, metric: string, from: Date, to: Date, step?: string): string {
  const p = new URLSearchParams({
    source,
    metric,
    from: from.toISOString(),
    to: to.toISOString(),
  });
  if (step) p.set("step", step);
  return `/api/series?${p.toString()}`;
}

// csvAllUrl downloads every channel of a source over the range in one CSV
// (metric omitted → server exports all channels, long format).
export function csvAllUrl(source: string, from: Date, to: Date, step?: string): string {
  const p = new URLSearchParams({ source, from: from.toISOString(), to: to.toISOString() });
  if (step) p.set("step", step);
  return `/api/export.csv?${p.toString()}`;
}

export function csvUrl(source: string, metric: string, from: Date, to: Date, step?: string): string {
  const p = new URLSearchParams({
    source,
    metric,
    from: from.toISOString(),
    to: to.toISOString(),
  });
  if (step) p.set("step", step);
  return `/api/export.csv?${p.toString()}`;
}

export function getSeries(source: string, metric: string, from: Date, to: Date, step?: string): Promise<SeriesResponse> {
  return getJSON<SeriesResponse>(seriesUrl(source, metric, from, to, step));
}

export function getConfig(): Promise<ConfigPayload> {
  return getJSON<ConfigPayload>("/api/config");
}

export async function login(username: string, password: string): Promise<string> {
  const resp = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!resp.ok) throw new Error("invalid credentials");
  const data = (await resp.json()) as { token: string };
  setToken(data.token);
  return data.token;
}

export async function putConfig(payload: Partial<ConfigPayload>, token: string): Promise<ConfigPayload> {
  const resp = await fetch("/api/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify(payload),
  });
  if (resp.status === 401) {
    clearToken();
    throw new Error("session expired — please log in again");
  }
  if (!resp.ok) throw new Error(`update failed (${resp.status})`);
  return resp.json() as Promise<ConfigPayload>;
}
