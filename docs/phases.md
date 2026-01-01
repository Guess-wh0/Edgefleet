# EdgeFleet Development Phases

EdgeFleet is built in explicit phases.
Each phase has a clear goal, strict boundaries, and exit criteria.
No phase is skipped. No phase is diluted.

---

## Phase 0 — Foundations (Thinking Before Code)

### Goal
Freeze the mental model of EdgeFleet before implementation.

### What exists in this phase
- Architecture definition
- Core principles
- Failure model
- Terminology and invariants
- Clear scope and non-goals

### What is explicitly NOT done
- No code
- No diagrams
- No abstractions for “future scale”

### Exit criteria
- `architecture.md`, `principles.md`, `failure-model.md`, `phases.md` exist
- You can explain EdgeFleet **verbally** in under 3 minutes
- Failure behavior is fully defined

> If it’s not written, it doesn’t exist.

---

## Phase 1 — Single Node, Single Control Plane

### Goal
Prove the core pull-based model works end-to-end.

### Scope
- One control plane
- One edge node
- One agent
- One desired state loop

### What is built
- Edge agent skeleton
- Control plane API (minimal)
- Heartbeat mechanism
- Desired vs actual state reconciliation
- Control-plane failure handling (per failure model)

### What is explicitly NOT done
- No clustering
- No persistence guarantees beyond local disk
- No auth beyond placeholders

### Exit criteria
- Edge continues running when control plane is down
- Edge reconciles correctly when control plane returns
- Behavior matches failure-model.md exactly

---

## Phase 2 — Multiple Edge Nodes

### Goal
Validate EdgeFleet as a coordination system, not a toy.

### Scope
- Multiple edge nodes
- Single control plane
- Independent failure of nodes

### What is built
- Node identity model
- State reporting per node
- Control plane handling concurrent edges
- Partial connectivity handling

### What is explicitly NOT done
- No leader election
- No edge-to-edge communication
- No auto-scaling logic

### Exit criteria
- One edge failing does not affect others
- Control plane handles staggered heartbeats
- System remains stable under partial outages

---

## Phase 3 — Robustness & Persistence

### Goal
Make EdgeFleet survive restarts and long outages.

### Scope
- Persistent desired state
- Persistent edge state
- Restart recovery

### What is built
- State storage in control plane
- Edge restart reconciliation
- Drift detection after downtime
- Replay-safe reconciliation

### What is explicitly NOT done
- No high availability control plane
- No distributed consensus

### Exit criteria
- Control plane restart does not break edges
- Edge restart does not corrupt state
- Reconciliation is idempotent

---

## Phase 4 — Security & Guardrails

### Goal
Prevent accidental or malicious misuse.

### Scope
- Authentication
- Authorization
- Trust boundaries

### What is built
- Edge identity & registration
- Signed desired state
- Basic auth between edge and control plane
- Rejection of untrusted commands

### What is explicitly NOT done
- No enterprise IAM
- No zero-trust marketing features

### Exit criteria
- Unauthorized edges cannot join
- Tampered desired state is rejected
- Security failures degrade safely

---

## Phase 5 — Extensibility (Only If Needed)

### Goal
Allow EdgeFleet to grow without collapsing its core.

### Scope
- Pluggable agents
- Extended state models
- Optional modules

### What is built
- Extension points
- Clear interfaces
- Documentation-first extensibility

### What is explicitly NOT done
- No plugin marketplace
- No premature generalization

### Exit criteria
- Core remains simple
- Extensions do not violate principles
- Failure model remains intact

---

## Phase Discipline Rule

You do not move forward because:
- You are excited
- You have time
- You saw a cool idea online

You move forward **only** when exit criteria are met.

EdgeFleet is built by subtraction, not addition.
