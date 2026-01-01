# EdgeFleet Principles

## 1. Edge-First Reliability
The system is designed assuming:
- Networks fail
- Control planes crash
- Latency is unpredictable

Edge nodes must continue operating regardless.

---

## 2. Pull Beats Push
All authority flows through pull:
- Edge nodes request state
- Control plane responds
- No unsolicited commands

This prevents cascading failure and thundering herds.

---

## 3. Determinism Over Intelligence
EdgeFleet prefers predictable behavior over “smart” behavior.

No heuristics.
No guesses.
No hidden automation.

---

## 4. Explicit Failure Is Better Than Silent Magic
Failures are modeled, documented, and expected.

If behavior is not defined in the failure model, it must not happen.

---

## 5. Control Plane Is Advisory, Not Authoritarian
The control plane defines intent.
The edge enforces reality.

Execution always happens at the edge.
