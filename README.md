# EdgeFleet

**Edge-first orchestration for unreliable networks.**

EdgeFleet is a lightweight edge orchestration system designed around a simple truth:
**the control plane may be fragile, but the edge must never be.**

This project focuses on first principles of edge systems—explicit failure handling, pull-based control, and deterministic behavior—without hiding behind heavyweight platforms.

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

If behavior isn’t documented, it doesn’t exist.

---

## Architecture (High Level)

- **Edge Node**
  Runs workloads and maintains local execution state.

- **Edge Agent**
  Pulls desired state, applies it locally, and reports actual state.

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

**Phase 0 — Foundations: COMPLETE**

Current state:
- Architecture frozen
- Principles defined
- Failure model documented
- Development phases locked
- No code yet (intentionally)

Next phase will introduce:
- A single edge node
- A single control plane
- A minimal reconciliation loop

---

## Documentation

- `docs/architecture.md` — system structure and responsibilities
- `docs/principles.md` — non-negotiable design rules
- `docs/failure-model.md` — what happens when things go wrong
- `docs/phases.md` — development roadmap and phase discipline

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

If you’re here to understand edge systems deeply—you’re in the right place.
