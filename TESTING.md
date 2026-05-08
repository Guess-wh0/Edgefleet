# EdgeFleet Test and Coverage Guide

This repository has two Go modules:

- `control-plane`
- `edge-agent`

Run tests inside each module directory so Go resolves the correct `go.mod`.

## Control Plane

```powershell
cd control-plane
go test ./...
```

Generate coverage:

```powershell
go test ./... -coverprofile=coverage
go tool cover -func=coverage
go tool cover -html=coverage -o coverage.html
```

How to view it:

1. Run the commands above.
2. Open `control-plane/coverage.html` in a browser.
3. Use the HTML report to inspect uncovered lines and functions.

## Edge Agent

```powershell
cd edge-agent
go test ./...
```

Generate coverage:

```powershell
go test ./... -coverprofile=coverage
go tool cover -func=coverage
go tool cover -html=coverage -o coverage.html
```

How to view it:

1. Run the commands above.
2. Open `edge-agent/coverage.html` in a browser.
3. Use the HTML report to inspect uncovered lines and functions.

## Security Coverage Included In Tests

- Signed desired state from the control plane
- Signature verification before edge apply
- Reject-and-log behavior for invalid signatures
- Replay protection through desired state version checks
- Token-authenticated edge requests
- Basic Auth on user and admin endpoints
