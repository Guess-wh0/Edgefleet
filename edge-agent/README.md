# Edge Node (EdgeFleet)

The edge node is the execution layer of EdgeFleet.

It is designed to operate in unreliable environments where connectivity,
latency, and central coordination cannot be assumed.

The edge node is **robust, disciplined, and deliberately limited**.

---

## What the Edge Node DOES

The edge node is responsible for **execution, not orchestration**.

Specifically, it:

- Runs a lightweight **edge agent**
- Executes workloads based on the **last known desired state**
- Continues operating even when the control plane is unavailable
- Maintains local runtime state
- Enforces local safety constraints (CPU, memory, basic sanity checks)
- Initiates all communication with the control plane (pull-based)

The edge node assumes the network will fail and plans accordingly.

---

## What the Edge Node NEVER Decides

The edge node does **not** make global or policy decisions.

It must never:

- Decide what the desired state *should be*
- Infer intent when the control plane is unreachable
- Change behavior due to loss of connectivity alone
- Coordinate with other edge nodes
- Elect leaders or form clusters
- Replay or invent instructions based on history
- Accept commands from any source other than the control plane

If behavior is not explicitly allowed, it does not happen.

---

## What the Edge Node Reports to the Control Plane

The edge node reports **facts**, not opinions.

It reports:

- Node identity (assigned by control plane)
- Heartbeats (liveness signals)
- Last applied desired state version
- Actual execution status
- Local errors or failures
- Optional metadata (hostname, architecture, capabilities)

It does **not**:
- Report guesses
- Report inferred global state
- Report speculative health

Silence is allowed. Fabrication is not.

---

## Failure Behavior (Summary)

- If the control plane disappears:
  - The edge node keeps running
  - No rollback occurs
  - No new behavior is invented

- When the control plane returns:
  - The edge node reconnects
  - Reports current state
  - Reconciles idempotently

The edge node never panics.
It degrades gracefully.

---

## Design Principle

> **The control plane may be fragile.
> The edge must never be.**

This file is a boundary.
Crossing it is a bug.
