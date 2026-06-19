// Static display metadata for the known channels. The server's /api/channels
// endpoint tells us which channels actually have data; this map tells us how to
// render each one (unit, group, axis scale, colour). Unknown channels fall back
// to a sensible default, so a future producer still charts without a code change.

export type Group = "pressure" | "temperature";

export interface ChannelMeta {
  unit: string;
  group: Group;
  log: boolean; // log Y axis (pressures span decades)
  color: string;
}

export const CHANNEL_META: Record<string, ChannelMeta> = {
  // Vacuum (Torr, log scale)
  LL: { unit: "Torr", group: "pressure", log: true, color: "#9aa0a6" },
  PC: { unit: "Torr", group: "pressure", log: true, color: "#4f9dff" },
  OC: { unit: "Torr", group: "pressure", log: true, color: "#ff7f50" },
  PREP: { unit: "Torr", group: "pressure", log: true, color: "#7ad97a" },
  // Temperature (Kelvin, linear) — colours echo the lab's Grafana board
  SORB: { unit: "K", group: "temperature", log: false, color: "#ffa500" },
  "1K Pot": { unit: "K", group: "temperature", log: false, color: "#7ad97a" },
  "He3 Pot": { unit: "K", group: "temperature", log: false, color: "#4f9dff" },
  STM: { unit: "K", group: "temperature", log: false, color: "#e066ff" },
};

const FALLBACK: ChannelMeta = { unit: "", group: "temperature", log: false, color: "#bbbbbb" };

export function metaFor(metric: string): ChannelMeta {
  return CHANNEL_META[metric] ?? FALLBACK;
}
