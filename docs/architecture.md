# EdgeFleet Architecture

## Overview
EdgeFleet is a pull-based edge orchestration system designed for unreliable networks and constrained environments.
The architecture is intentionally minimal, explicit, and edge-first.

The control plane may be fragile.
Edge nodes must not be.

---

## Components

### Edge Node
A physical or virtual device deployed at the edge.
Examples include microcontrollers, embedded devices, or general-purpose computers.

Each edge node:
- Runs a single EdgeFleet agent
- Executes workloads locally
- Maintains its own runtime state
- Never depends on continuous control-plane availability

---

### Edge Agent
A lightweight process running on each edge node.

Responsibilities:
- Pull desired state from the control plane
- Apply desired state locally
- Report actual state and health
- Enforce local safety constraints

The agent is deterministic and idempotent.

---

### Control Plane
A centralized service responsible for:
- Computing desired state
- Maintaining global intent
- Observing reported edge state

The control plane:
- Does not push commands
- Does not assume availability
- Is not on the execution path

---

## Communication Model

- **Pull-based**
- Edge nodes initiate all communication
- No inbound connections to edge nodes are required
- Loss of connectivity does not halt execution

---

## Data Flow

1. Edge agent reports current state
2. Control plane computes desired state
3. Edge agent pulls and reconciles desired state
4. Edge continues execution independently

---

## Non-Goals

- Replacing Kubernetes
- Real-time control guarantees
- Automatic failover between control planes
- Decision-making at the edge beyond defined bounds
