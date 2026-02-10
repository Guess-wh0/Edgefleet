# EdgeFleet — Control Plane

Minimal control plane for EdgeFleet.
Responsible for **node identity, liveness tracking, and coordination primitives**.

This is **not** a scheduler.
This is **not** a leader.
This is **not** smart.

It exists to tell the truth about nodes.

---

## Responsibilities (Phase 1–2)

### What it DOES
- Accept node registration
- Persist node identity
- Track heartbeats per node
- Mark nodes `available` / `unavailable` based on time
- Serve empty desired state (by design)

### What it explicitly does NOT do
- Leader election
- Scheduling
- Workload orchestration
- Edge-to-edge communication
- Retry/backoff intelligence

Coordination first. Intelligence later.

---

## Tech Stack

- Language: **Go**
- Storage: **SQLite (pure Go driver)**
- Concurrency model: **per-request + periodic sweep**
- OS: Linux / macOS / Windows (no CGO)

---

## Project Structure

```

control-plane/
├── main.go
├── go.mod
├── edgefleet.db   # created at runtime

````

---

## Prerequisites

- Go **1.21+**
- No C compiler required
- No Docker required
- No cloud dependencies

Verify Go:
```bash
go version
````

---

## Setup

Clone the repository and move into the control plane directory:

```bash
cd control-plane
```

Resolve dependencies:

```bash
go mod tidy
```

---

## Start the Control Plane

Run directly:

```bash
go run .
```

Or build once and run:

```bash
go build -o control-plane
./control-plane
```

Expected output:

```
Control Plane starting on :8080
```

SQLite database (`edgefleet.db`) is created automatically on first run.

---

## API Endpoints (Phase 1–2)

### Health Check

```
GET /health
```

Example:

```bash
curl http://localhost:8080/health
```

Response:

```
nodes=3
```

---

### Register Node

```
POST /register
```

Headers:

* `X-Node-Hostname`
* `X-Node-Arch`

Example:

```bash
curl -X POST http://localhost:8080/register \
  -H "X-Node-Hostname: node-A" \
  -H "X-Node-Arch: amd64"
```

Response:

```
<node-id>
```

---

### Heartbeat

```
POST /heartbeat
```

Headers:

* `X-Node-ID`

Example:

```bash
curl -X POST http://localhost:8080/heartbeat \
  -H "X-Node-ID: <node-id>"
```

Response:

```
ack
```

---

### Desired State (intentionally empty)

```
GET /desired-state/{nodeID}
```

Example:

```bash
curl http://localhost:8080/desired-state/<node-id>
```

Response:

```
(empty)
```

---

## Liveness Model

* Heartbeats update `last_heartbeat`
* Periodic sweep runs every few seconds
* Nodes with stale heartbeats are marked `unavailable`
* A new heartbeat restores `available`

No node is ever deleted.

---

## Data Model

SQLite table: `nodes`

```sql
nodes (
  node_id TEXT PRIMARY KEY,
  last_heartbeat TIMESTAMP,
  status TEXT,
  hostname TEXT,
  arch TEXT
)
```

---

## Operating Assumptions

* Partial connectivity is normal
* Nodes may flap independently
* Control plane must remain calm under chaos
* Identity is sacred and never auto-regenerated

---

## Phase Status

* Phase 1: ✅ Complete
* Phase 2 (current):

  * Identity hardening: ✅
  * Multi-node simulation: ✅
  * Idempotent registration: ⏳ next

---

## Philosophy

If the control plane panics, the system is already dead.

This code favors:

* determinism
* explicit state
* boring correctness

Everything else comes later.
