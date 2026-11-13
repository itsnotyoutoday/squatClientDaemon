# ADR 0001: License is GPL-3.0-or-later

Status: accepted (2026-11-12)

Decision: both repositories (`squatclient`, `squatClientDaemon`) are licensed **GPL-3.0-or-later**.
This replaces the earlier MIT choice.

Why: anyone who builds on or distributes SquatClient/squatterd must share their changes under the same terms,
so improvements flow back to the project.

Consequences:
- Every source file carries `SPDX-License-Identifier: GPL-3.0-or-later` (added by `scripts/add-spdx.sh`; new files must include it).
- Dependencies must be GPL-3.0 compatible (permissive licences such as MIT, BSD, ISC, Apache-2.0 are fine; no GPL-incompatible or
  proprietary-only dependencies).
- The TF-style language remains an original clean-room implementation (no TinyFugue code or bundled files); that is now a
  design choice, no longer a licensing necessity.
- Contributions are accepted under the same licence (inbound = outbound); a CONTRIBUTING note says so.
- Note: GPL-3.0 does not close the "network service" loophole; if you want modifications of the *daemon* offered over a network
  to be shared too, the daemon could move to AGPL-3.0-or-later (revisit with a new ADR).
