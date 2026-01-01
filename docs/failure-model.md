# Failure Model

This document defines how EdgeFleet behaves when things go wrong.
Any behavior not listed here is considered a bug.

---

## Invariant

**An edge node must never stop or change behavior solely because the control plane is unavailable.**

---

## Failure Scenarios

### Control Plane Unavailable (Short-Term)
Examples:
- Network partition
- Control plane crash
- Timeout or connection refusal

Edge behavior:
- Continue operating using last known desired state
- Enter degraded-connectivity mode
- Reduce heartbeat frequency
- Buffer state reports locally
- Do not retry aggressively

---

### Control Plane Unavailable (Extended)
Edge behavior:
- Continue execution indefinitely
- Enforce local safety constraints
- Do not escalate, fail over, or invent new behavior
- Do not accept external instructions

---

### Control Plane Recovery
When connectivity is restored:
- Edge initiates reconnection
- Sends buffered state and drift information
- Awaits new desired state
- Reconciles idempotently

No replay of stale commands.
No assumptions.

---

## Explicitly Forbidden Behaviors

- Halting workloads due to control-plane failure
- Switching to backup masters
- Accepting ad-hoc commands
- Reconstructing state from history
- Acting outside this failure model

---

## Design Intent
EdgeFleet prioritizes survivability over coordination.
Correctness degrades gracefully.
Availability does not.
