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
