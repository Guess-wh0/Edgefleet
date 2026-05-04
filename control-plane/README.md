# EdgeFleet - Control Plane

Minimal control plane for EdgeFleet.
Responsible for **node identity, liveness tracking, desired-state storage, and security guardrails**.

This is **not** a scheduler.
This is **not** a leader.
This is **not** smart.

It exists to tell the truth about nodes.

---

## Responsibilities

### What it DOES
- Accept edge registration
- Issue `node_id` and `node_secret`
- Persist node identity and secrets
- Track heartbeats per node
- Mark nodes `active` / `unknown` based on time
- Serve desired state to authenticated edges
- Require Basic Auth on user/admin endpoints

### What it explicitly does NOT do
- Leader election
- Scheduling
- Workload orchestration
- Edge-to-edge communication
- Retry/backoff intelligence
- OAuth or JWT flows

Coordination first. Intelligence later.

---

## Tech Stack

- Language: **Go**
- Storage: **SQLite (pure Go driver)**
- Concurrency model: **per-request + periodic sweep**
- OS: Linux / macOS / Windows (no CGO)

---

## Project Structure

```text
control-plane/
|-- main.go
|-- go.mod
`-- edgefleet.db   # created at runtime
```

---

## Prerequisites

- Go **1.21+**
- No C compiler required
- No Docker required
- No cloud dependencies

Verify Go:

```bash
go version
```

---

## Setup

Move into the control-plane directory:

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

```text
Control Plane starting on :8080
```

SQLite database (`edgefleet.db`) is created automatically on first run.

---

## Authentication Model

### Edge -> Control

- `POST /register` is open so a new edge can bootstrap identity
- Control plane responds with:
  - `node_id`
  - `node_secret`
- Edge stores both locally and sends them on every later request:
  - `X-Node-ID`
  - `X-Node-Token`

### User -> Control

- User/admin endpoints require HTTP Basic Auth
- Defaults are:
  - user: `admin`
  - password: `edgefleet`
- Override with environment variables:
  - `CONTROL_PLANE_USER`
  - `CONTROL_PLANE_PASSWORD`

Example:

```powershell
$env:CONTROL_PLANE_USER="admin"
$env:CONTROL_PLANE_PASSWORD="change-me"
go run .
```

---

## API Endpoints

### Health Check

```text
GET /health
```

Requires Basic Auth.

Example:

```bash
curl -u admin:edgefleet http://localhost:8080/health
```

Response:

```text
nodes=3
```

---

### Register Node

```text
POST /register
```

Headers:

- `X-Node-Hostname`
- `X-Node-Arch`

Example:

```bash
curl -X POST http://localhost:8080/register \
  -H "X-Node-Hostname: node-A" \
  -H "X-Node-Arch: amd64"
```

Response:

```json
{
  "node_id": "f2d8d2c8-...",
  "node_secret": "8c9d..."
}
```

---

### Heartbeat

```text
POST /heartbeat
```

Headers:

- `X-Node-ID`
- `X-Node-Token`

Example:

```bash
curl -X POST http://localhost:8080/heartbeat \
  -H "X-Node-ID: <node-id>" \
  -H "X-Node-Token: <node-secret>"
```

Response:

```text
ack
```

---

### Desired State

```text
GET /desired-state/{nodeID}
```

Headers:

- `X-Node-ID`
- `X-Node-Token`

Example:

```bash
curl http://localhost:8080/desired-state/<node-id> \
  -H "X-Node-ID: <node-id>" \
  -H "X-Node-Token: <node-secret>"
```

Response:

```json
{"version":4,"payload":"handler-check"}
```

If no desired state exists yet, the response body is empty.

---

### Set Desired State

```text
POST /debug/set-desired?nodeID={nodeID}&version={version}
```

Requires Basic Auth.

Example:

```bash
curl -X POST "http://localhost:8080/debug/set-desired?nodeID=<node-id>&version=5" \
  -u admin:edgefleet \
  -d '{"version":5,"payload":"deploy"}'
```

---

### List Nodes

```text
GET /nodes
```

Requires Basic Auth.

Example:

```bash
curl -u admin:edgefleet http://localhost:8080/nodes
```

---

## Liveness Model

- Heartbeats update `last_heartbeat`
- Periodic sweep runs every few seconds
- Nodes with stale heartbeats are marked `unknown`
- A new heartbeat restores `active`

No node is ever deleted.

---

## Data Model

SQLite table: `nodes`

```sql
nodes (
  node_id TEXT PRIMARY KEY,
  node_secret TEXT,
  last_heartbeat TIMESTAMP,
  status TEXT,
  hostname TEXT,
  arch TEXT
)
```

SQLite table: `desired_state`

```sql
desired_state (
  node_id TEXT PRIMARY KEY,
  version INTEGER,
  payload TEXT
)
```

---

## Operating Assumptions

- Partial connectivity is normal
- Nodes may flap independently
- Control plane must remain calm under chaos
- Identity is sacred and never auto-regenerated without an explicit re-registration path

---

## Phase Status

- Phase 3: complete
- Phase 4 in progress:
  - Edge identity and token auth: complete
  - Basic Auth on admin endpoints: complete
  - Signed desired state: pending

---

## Philosophy

If the control plane panics, the system is already dead.

This code favors:

- determinism
- explicit state
- boring correctness

Everything else comes later.
