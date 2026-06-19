import { useEffect, useState } from "react";
import { ConfigPayload, Threshold, clearToken, getConfig, getToken, login, putConfig } from "../api";

// numToStr / strToNum bridge the nullable numeric threshold fields and text inputs.
const numToStr = (n: number | null): string => (n === null ? "" : String(n));
const strToNum = (s: string): number | null => (s.trim() === "" ? null : Number(s));

export function AdminPanel({ onConfigChange }: { onConfigChange?: () => void }) {
  const [token, setTok] = useState<string | null>(getToken());
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [config, setConfig] = useState<ConfigPayload | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    getConfig().then(setConfig).catch((e) => setErr(String(e)));
  }, []);

  async function handleLogin(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    try {
      const t = await login(username, password);
      setTok(t);
      setPassword("");
      setMsg("Logged in.");
    } catch {
      setErr("Invalid credentials.");
    }
  }

  function handleLogout() {
    clearToken();
    setTok(null);
    setMsg(null);
  }

  function updateThreshold(i: number, patch: Partial<Threshold>) {
    if (!config) return;
    const thresholds = config.thresholds.map((t, idx) => (idx === i ? { ...t, ...patch } : t));
    setConfig({ ...config, thresholds });
  }

  async function handleSave() {
    if (!config || !token) return;
    setErr(null);
    setMsg(null);
    try {
      const updated = await putConfig(
        { sampling_interval_seconds: config.sampling_interval_seconds, thresholds: config.thresholds },
        token,
      );
      setConfig(updated);
      setMsg("Configuration saved.");
      onConfigChange?.();
    } catch (e) {
      setErr(String(e));
      if (String(e).includes("log in again")) setTok(null);
    }
  }

  return (
    <div className="card admin">
      <div className="card-head">
        <h2>Admin · configuration</h2>
        {token && <button className="btn-secondary" onClick={handleLogout}>Log out</button>}
      </div>

      {!token && (
        <form className="login" onSubmit={handleLogin}>
          <p className="muted">Charts above are public. Log in to change the sampling interval and alert thresholds.</p>
          <input placeholder="username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
          <input placeholder="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
          <button type="submit">Log in</button>
        </form>
      )}

      {token && config && (
        <div className="config-editor">
          <label className="field">
            <span>Sampling interval (seconds)</span>
            <input
              type="number"
              min={1}
              max={3600}
              value={config.sampling_interval_seconds}
              onChange={(e) => setConfig({ ...config, sampling_interval_seconds: Number(e.target.value) })}
            />
          </label>

          <table className="thresholds">
            <thead>
              <tr><th>metric</th><th>min</th><th>max</th><th>enabled</th></tr>
            </thead>
            <tbody>
              {config.thresholds.map((t, i) => (
                <tr key={t.metric}>
                  <td>{t.metric}</td>
                  <td><input value={numToStr(t.min)} onChange={(e) => updateThreshold(i, { min: strToNum(e.target.value) })} /></td>
                  <td><input value={numToStr(t.max)} onChange={(e) => updateThreshold(i, { max: strToNum(e.target.value) })} /></td>
                  <td><input type="checkbox" checked={t.enabled} onChange={(e) => updateThreshold(i, { enabled: e.target.checked })} /></td>
                </tr>
              ))}
            </tbody>
          </table>

          <button onClick={handleSave}>Save configuration</button>
        </div>
      )}

      {msg && <div className="ok">{msg}</div>}
      {err && <div className="error">{err}</div>}
    </div>
  );
}
