import { useEffect, useMemo, useState } from "react";
import { Channel, getChannels } from "./api";
import { metaFor } from "./channels";
import { ChartCard } from "./components/ChartCard";
import { AdminPanel } from "./components/AdminPanel";
import { PRESETS, RangePreset, rangeFor } from "./timerange";

const LIVE_REFRESH_MS = 5000;

// Preferred display order; any present-but-unknown channel is appended.
const PRESSURE_ORDER = ["LL", "PC", "OC", "PREP"];
const TEMPERATURE_ORDER = ["SORB", "1K Pot", "He3 Pot", "STM"];

function orderMetrics(present: string[], group: "pressure" | "temperature"): string[] {
  const preferred = group === "pressure" ? PRESSURE_ORDER : TEMPERATURE_ORDER;
  const inGroup = present.filter((m) => metaFor(m).group === group);
  const known = preferred.filter((m) => inGroup.includes(m));
  const unknown = inGroup.filter((m) => !preferred.includes(m));
  return [...known, ...unknown];
}

export default function App() {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [preset, setPreset] = useState<RangePreset>(PRESETS[1]); // 1h
  const [live, setLive] = useState(true);
  const [now, setNow] = useState<number>(Date.now());
  const [refreshSignal, setRefresh] = useState(0);

  // Poll channels (latest values + discovery) and, in live mode, advance "now".
  useEffect(() => {
    let timer: number | undefined;
    const tick = () => {
      getChannels().then(setChannels).catch(() => {});
      if (live) setNow(Date.now());
    };
    tick();
    if (live) timer = window.setInterval(tick, LIVE_REFRESH_MS);
    return () => {
      if (timer) window.clearInterval(timer);
    };
  }, [live, refreshSignal]);

  const source = channels[0]?.source ?? "unisoku-stm";
  const present = useMemo(() => channels.map((c) => c.metric), [channels]);
  const pressureMetrics = useMemo(() => orderMetrics(present, "pressure"), [present]);
  const temperatureMetrics = useMemo(() => orderMetrics(present, "temperature"), [present]);

  const { from, to } = rangeFor(preset, now);
  const latest = new Map(channels.map((c) => [c.metric, c]));

  return (
    <div className="app">
      <header className="topbar">
        <div>
          <h1>Lab Monitor</h1>
          <span className="subtitle">Unisoku STM · vacuum &amp; cryogenics · {source}</span>
        </div>
        <div className="controls">
          <div className="ranges">
            {PRESETS.map((p) => (
              <button
                key={p.label}
                className={p.label === preset.label ? "range active" : "range"}
                onClick={() => setPreset(p)}
              >
                {p.label}
              </button>
            ))}
          </div>
          <label className="live-toggle">
            <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
            live
          </label>
          {!live && (
            <button className="btn-secondary" onClick={() => { setNow(Date.now()); setRefresh((s) => s + 1); }}>
              refresh
            </button>
          )}
        </div>
      </header>

      <section className="stats">
        {[...pressureMetrics, ...temperatureMetrics].map((m) => {
          const c = latest.get(m);
          const meta = metaFor(m);
          const val = c
            ? meta.log
              ? c.last_value.toExponential(2)
              : c.last_value.toFixed(2)
            : "—";
          return (
            <div className="stat" key={m}>
              <span className="stat-dot" style={{ background: meta.color }} />
              <span className="stat-name">{m}</span>
              <span className="stat-val">{val} <span className="unit">{meta.unit}</span></span>
            </div>
          );
        })}
      </section>

      <main className="charts">
        <ChartCard
          title="Pressure"
          source={source}
          metrics={pressureMetrics}
          from={from}
          to={to}
          log
          unit="Torr"
          refreshSignal={refreshSignal}
        />
        <ChartCard
          title="Temperature"
          source={source}
          metrics={temperatureMetrics}
          from={from}
          to={to}
          log={false}
          unit="K"
          refreshSignal={refreshSignal}
        />
      </main>

      <AdminPanel onConfigChange={() => setRefresh((s) => s + 1)} />

      <footer className="foot">
        Public read-only dashboard · data retained indefinitely in TimescaleDB ·
        <a href="/metrics"> /metrics</a> · <a href="/healthz">/healthz</a>
      </footer>
    </div>
  );
}
