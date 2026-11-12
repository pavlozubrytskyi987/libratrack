# LibraTrack

A gRPC-based library inventory service written in Go. Tracks physical book copies and their statuses (available, borrowed, lost).

## What it does

- **Server** manages book copies in memory with status tracking and live notifications
- **Client** talks to the server using all four gRPC call patterns

### RPC patterns used

| Pattern | RPC | What it does |
|---------|-----|-------------|
| Unary | `GetCopy`, `RegisterCopy`, `UpdateCopyStatus` | Basic CRUD for book copies |
| Server streaming | `WatchCopyStatus` | Live status updates pushed to the client |
| Client streaming | `BulkRegisterCopies` | Register many copies in one stream |
| Bidirectional streaming | `SyncInventory` | Two-way sync between library branches |

## How to run

You need Go 1.22+.

**Start the server:**
```bash
go run ./cmd/inventory-server -simulate
```

The `-simulate` flag auto-toggles statuses every 2 seconds so you can see live updates.

**In another terminal, try the client:**
```bash
# register a new copy
go run ./cmd/inventory-client register book-1 INV-001

# get a copy by id
go run ./cmd/inventory-client get copy-1

# change status
go run ./cmd/inventory-client set-status copy-1 BORROWED

# watch live status changes (server streaming)
go run ./cmd/inventory-client -max 3 watch copy-1

# bulk register 5 copies (client streaming)
go run ./cmd/inventory-client bulk book-7 5

# branch sync (bidi streaming)
go run ./cmd/inventory-client sync
```

## Project structure

```
proto/                  protobuf definition
gen/                    generated Go code from proto
cmd/inventory-server/   server entry point
cmd/inventory-client/   client entry point
internal/server/        RPC handlers
internal/store/         in-memory storage + pub/sub
internal/auth/          JWT auth + RBAC interceptors
internal/middleware/     logging + panic recovery
internal/tlsconf/       mTLS config helpers
```

## Optional features

- **JWT auth**: `go run ./cmd/inventory-server -auth` (use `go run ./cmd/mint-token` to get a token)
- **mTLS**: `go run ./cmd/gencerts` first, then `go run ./cmd/inventory-server -tls -auth`
- **REST gateway**: `go run ./cmd/gateway` exposes the same service over HTTP

## Tests

```bash
go test ./... -race
```
