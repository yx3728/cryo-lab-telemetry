# WORKLOG

Append-only, timestamped record of the build: findings, components completed,
test numbers, deploy verification, and scope decisions.

---

## 2026-06-18 — Step 1: skeleton, docs, git init

- **Toolchain (dev Mac, Apple Silicon / arm64):** Go 1.26.4, Node 24.11, Python
  3.12, Docker 29.5 via colima, Docker Compose 5.1.4. ARM dev box matches the
  `t4g.small` ARM deploy target, so images build natively.
- **Grafana shape check (optional, succeeded):** fetched the lab's public
  dashboard API
  (`/api/public/dashboards/9a1b0a82458c4240a22e60e541a20587`). Confirmed:
  - Stat panels `LL (Torr)`, `PC (Torr)`, `OC (Torr)`; time-series panels
    `Temperature` and `Pressure (Torr)`.
  - Vacuum channels **LL, PC, OC, PREP** (Torr, **log scale**).
  - Temperature channels **SORB, 1K Pot, He3 Pot, STM** (Kelvin, linear).
  This matches the build spec exactly; the mock uses the documented numeric
  shapes. **The runtime does not depend on Grafana.**
- **Scope decision:** MVP = existing few-second multi-channel vacuum + temp data,
  end-to-end with alerting + remote config. The ~2 ms / 50 Hz fast STM-current
  channel is on a separate, not-connected computer → **out of scope, but the
  ingest path and load test are built to accept it as a future per-source
  producer**. Never claimed live.
- **Reliability decision:** build exactly retry+backoff, offline disk buffer,
  idempotent ingest, concurrent-producer correctness. No consensus / broker /
  sharding / k8s.
- Repo: `git init` on branch `main`, local identity set to `yx3728`. Wrote
  `README.md`, `PLAN.md`, `.gitignore` (excludes secrets/`node_modules`/builds),
  `.env.example` (all config via env, no secrets in code). Directory skeleton:
  `server/ collector/ mock/ web/ deploy/`.
