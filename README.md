# EdgeFleet

**Edge-first orchestration for unreliable networks.**

EdgeFleet is a lightweight edge orchestration system designed around a simple truth:
**the control plane may be fragile, but the edge must never be.**

This project focuses on first principles of edge systems: explicit failure handling, pull-based control, and deterministic behavior without hiding behind heavyweight platforms.

---

## Why EdgeFleet Exists

Most orchestration systems assume:
- Reliable networks
- Always-on control planes
- Centralized authority

Edge environments offer none of these guarantees.

EdgeFleet is built for:
- Intermittent connectivity
- Constrained devices
- Long-lived edge autonomy
- Clear, documented failure behavior

No magic. No guesswork.

---

## Core Idea

- **Pull-based model**: edge nodes pull desired state
- **Edge-first execution**: work continues even if the control plane disappears
- **Explicit failure model**: all failure behavior is written down
- **Deterministic reconciliation**: no heuristics, no surprises

If behavior is not documented, it does not exist.

---

## Architecture (High Level)

- **Edge Node**
  Runs workloads and maintains local execution state.

- **Edge Agent**
  Pulls desired state, applies it locally, authenticates itself to the control plane, and reports actual state.

- **Control Plane**
  Computes desired state and observes the system.
  It does not push commands and is not on the execution path.

Communication is always initiated by the edge.

---

## Failure Philosophy

> **An edge node must never stop or change behavior solely because the control plane is unavailable.**

When the control plane goes down:
- Edge nodes continue running
- Last known desired state is preserved
- Connectivity degrades gracefully
- Reconciliation resumes when control returns

All failure behavior is explicitly defined.
Anything else is a bug.

---

## Project Status

**Phase 4 - Security & Guardrails**

Current state:
- State storage in control plane
- Edge restart reconciliation
- Drift detection after downtime
- Replay-safe reconciliation
- Edge identity with `node_id` and `node_secret`
- Token-authenticated edge requests
- Signed desired state delivered with HMAC
- Edge verifies signatures before applying
- Basic Auth on control-plane user/admin endpoints

Security test coverage is documented in `TESTING.md`.

---

## Running Tests

Run the module tests from each Go module directory:

```powershell
cd control-plane
go test ./...
```

```powershell
cd edge-agent
go test ./...
```

Generate a coverage profile inside either module:

```powershell
go test ./... -coverprofile=coverage
go tool cover -func=coverage
go tool cover -html=coverage -o coverage.html
```

Open `coverage.html` in a browser to inspect line-by-line coverage.

---

## Documentation

- `docs/architecture.md` - system structure and responsibilities
- `docs/principles.md` - non-negotiable design rules
- `docs/failure-model.md` - what happens when things go wrong
- `docs/phases.md` - development roadmap and phase discipline
- `TESTING.md` - `go test` coverage commands and report viewing steps

Read these before writing or reviewing code.

---

## Non-Goals

EdgeFleet is **not**:
- A Kubernetes replacement
- A real-time control system
- A cloud-first platform
- A demo project optimized for hype

It is a learning-first, correctness-driven edge system.

---

## License

Apache License 2.0

You are free to use, modify, and distribute this project under the terms of the license.

---

## Final Note

EdgeFleet is built slowly on purpose.
Correctness before scale.
Clarity before features.

If you are here to understand edge systems deeply, you are in the right place.
