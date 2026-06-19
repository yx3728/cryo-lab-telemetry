import { useEffect, useState } from "react";
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { csvUrl, getSeries } from "../api";
import { metaFor } from "../channels";
import { formatTick } from "../timerange";

interface Props {
  title: string;
  source: string;
  metrics: string[];
  from: Date;
  to: Date;
  log: boolean;
  unit: string;
  refreshSignal: number; // bump to force a refetch (live mode)
}

type Row = { ts: number } & Record<string, number>;

// merge per-metric series (which share aligned time buckets) into one row array.
function mergeSeries(byMetric: Record<string, { ts: string; value: number }[]>): Row[] {
  const rows = new Map<number, Row>();
  for (const [metric, points] of Object.entries(byMetric)) {
    for (const p of points) {
      const t = Date.parse(p.ts);
      const row = rows.get(t) ?? ({ ts: t } as Row);
      row[metric] = p.value;
      rows.set(t, row);
    }
  }
  return [...rows.values()].sort((a, b) => a.ts - b.ts);
}

export function ChartCard({ title, source, metrics, from, to, log, unit, refreshSignal }: Props) {
  const [data, setData] = useState<Row[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    Promise.all(metrics.map((m) => getSeries(source, m, from, to).then((r) => [m, r.points] as const)))
      .then((entries) => {
        if (cancelled) return;
        setData(mergeSeries(Object.fromEntries(entries)));
        setError(null);
      })
      .catch((e) => !cancelled && setError(String(e)))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // from/to are Date objects; key on their time value to avoid identity churn.
  }, [source, metrics.join(","), from.getTime(), to.getTime(), refreshSignal]);

  const spanMs = to.getTime() - from.getTime();
  const yTickFmt = (v: number) => (log ? v.toExponential(1) : v.toFixed(1));

  return (
    <div className="card">
      <div className="card-head">
        <h2>{title} <span className="unit">({unit})</span></h2>
        {loading && <span className="badge">updating…</span>}
      </div>
      {error && <div className="error">{error}</div>}
      <ResponsiveContainer width="100%" height={260}>
        <LineChart data={data} margin={{ top: 8, right: 16, bottom: 4, left: 8 }}>
          <CartesianGrid stroke="#23272e" strokeDasharray="3 3" />
          <XAxis
            dataKey="ts"
            type="number"
            scale="time"
            domain={[from.getTime(), to.getTime()]}
            tickFormatter={(t) => formatTick(t, spanMs)}
            stroke="#8a909a"
            fontSize={11}
            minTickGap={40}
          />
          <YAxis
            scale={log ? "log" : "linear"}
            domain={["auto", "auto"]}
            tickFormatter={yTickFmt}
            stroke="#8a909a"
            fontSize={11}
            width={56}
          />
          <Tooltip
            contentStyle={{ background: "#11151a", border: "1px solid #2a2f37", fontSize: 12 }}
            labelFormatter={(t) => new Date(Number(t)).toLocaleString()}
            formatter={(v: number, name) => [log ? v.toExponential(2) : v.toFixed(3), name]}
          />
          <Legend />
          {metrics.map((m) => (
            <Line
              key={m}
              type="monotone"
              dataKey={m}
              stroke={metaFor(m).color}
              dot={false}
              isAnimationActive={false}
              connectNulls={false}
              strokeWidth={1.6}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
      <div className="csv-row">
        <span>CSV:</span>
        {metrics.map((m) => (
          <a key={m} href={csvUrl(source, m, from, to)} className="csv-link">
            {m}
          </a>
        ))}
      </div>
    </div>
  );
}
