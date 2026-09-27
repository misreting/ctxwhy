# ctxwhy

`context.WithTimeout`, plus knowing which nested timeout actually fired, and
whether a deeper one was ever going to be able to.

```
=== plain context.WithTimeout + context.Cause ===
operation failed: context deadline exceeded (cause: context deadline exceeded)
```

That's a real overall budget (50ms) wrapping a real per-attempt timeout that
looks generous (500ms). One of them fired. `context.Cause` — Go's own answer
to "why was this canceled" — cannot tell you which, because both were
created with plain `context.WithTimeout`, so both carry the same
`context.DeadlineExceeded` sentinel as their cause.

## The problem

Nested timeouts are ordinary: a request handler sets an overall budget,
middleware or a client wraps each retry attempt in its own timeout that
"looks" well within budget, and in production the outer one fires while the
inner one was never close. When it does, all you get back is:

```go
outer, _ := context.WithTimeout(bg, 50*time.Millisecond)   // overall budget
inner, _ := context.WithTimeout(outer, 500*time.Millisecond) // per-attempt
<-inner.Done()
inner.Err()               // context.DeadlineExceeded
context.Cause(inner)       // context.DeadlineExceeded — same thing
```

`Err()` and `Cause()` agree, and are equally useless: neither says *which*
timeout, *where it was created*, or *what its budget was*. In a service with
a handful of nested budgets (ingress deadline, per-dependency timeout,
per-retry timeout), that's a real debugging problem, not a hypothetical one.

### The obvious workaround doesn't fully work

Go 1.21 added `context.WithTimeoutCause`/`WithDeadlineCause`, and the
`context.Cause` chain does correctly propagate whichever ancestor actually
fired — *if* every call site remembers to pass a distinct, descriptive
cause:

```go
outer, _ := context.WithTimeoutCause(bg, 50*time.Millisecond, errors.New("request budget"))
inner, _ := context.WithTimeoutCause(outer, 500*time.Millisecond, errors.New("per-attempt timeout"))
```

This works, but it requires manual, disciplined effort at every single call
site, forever, across a whole codebase — and it still gives you no way to
ask, **before anything fires**, "is my per-attempt timeout dead code because
an ancestor's budget is always tighter?" `context.Context` is intentionally
opaque; there is no stdlib way to walk a context's ancestor deadlines.

### What's already out there

This area isn't empty, so before writing this, the existing wrappers were
read (not guessed at):

| Package | What it actually does |
| --- | --- |
| [`mongodb-labs/migration-verifier/contextplus`](https://github.com/mongodb-labs/migration-verifier/blob/main/contextplus/contextplus.go) | Wraps `WithDeadlineCause`/`WithTimeoutCause`; **requires the caller to pass a `cause error` argument at every call site**, and only stringifies the duration into that cause — no call-site capture, no chain inspection. |
| [`gostdlib/base`](https://github.com/gostdlib/base) `context` package | A thin, documented alias layer over stdlib `context` (its `WithTimeoutCause` is literally `return context.WithTimeoutCause(...)`). Excellent structured-error/telemetry story elsewhere in the module, but no timeout-chain attribution at all. |
| [`alexfalkowski/go-service`](https://github.com/alexfalkowski/go-service) `context` package | Same pattern: "thin aliases/wrappers around context ... preserving the exact semantics of the standard library". No attribution beyond what stdlib gives you. |
| [`k1LoW/donegroup`](https://github.com/k1LoW/donegroup) | Solves a different problem entirely: running cleanup goroutines when a context is canceled (`sync.WaitGroup` + `<-ctx.Done()`). Not concerned with timeouts or attribution. |

None of the four does automatic file:line + duration capture, and none
exposes a way to inspect the chain of nested deadlines to find the binding
one before it fires. That's the gap this package fills — narrowly.

## Install

```sh
go get github.com/agentscope-ai-java/ctxwhy
```

Go 1.23+. No runtime dependencies.

## Use

`ctxwhy.WithTimeout` / `ctxwhy.WithDeadline` are drop-in replacements for
their stdlib counterparts:

```go
import "github.com/agentscope-ai-java/ctxwhy"

outer, cancel := ctxwhy.WithTimeout(ctx, 50*time.Millisecond)   // overall budget
defer cancel()
inner, cancel := ctxwhy.WithTimeout(outer, 500*time.Millisecond) // per-attempt
defer cancel()
```

Ask which one is actually going to bind, before either fires:

```go
fmt.Print(ctxwhy.Explain(inner))
```

```
ctxwhy: 2 tracked timeout(s), outermost first:
  - 50ms timeout set at main.go:25 (main.main), deadline 23:44:55.615 [BINDING — this one wins]
  - 500ms timeout set at main.go:27 (main.main), deadline 23:44:56.065 [shadowed]
```

And once it fires, ask which one actually did:

```go
<-inner.Done()
t, ok := ctxwhy.Cause(inner)
if ok {
    log.Printf("timed out: %s", t) // "50ms timeout set at main.go:25 (main.main), deadline ..."
}
```

`inner.Err()` and `errors.Is(inner.Err(), context.DeadlineExceeded)` behave
exactly as they do for a plain `context.WithTimeout` — `ctxwhy` only adds
information, it doesn't change context semantics.

Full runnable version: `go run ./examples/repro` (output above is real,
pasted from an actual run).

## API

| Function | What it returns |
| --- | --- |
| `WithTimeout(parent, d)` | Same as `context.WithTimeout`, plus tracking. |
| `WithDeadline(parent, t)` | Same as `context.WithDeadline`, plus tracking. |
| `Cause(ctx) (Timeout, bool)` | The tracked `Timeout` whose deadline actually fired and produced `ctx`'s cancellation cause. `ok` is `false` if `ctx` isn't done, was canceled explicitly (not by a deadline), or the firing deadline wasn't created by this package. |
| `Chain(ctx) []Timeout` | Every tracked timeout on `ctx`, outermost first. Only sees ones created via this package. |
| `Binding(ctx) (Timeout, bool)` | The tracked timeout with the earliest absolute deadline — the one that will (or did) actually fire. Safe to call before anything has fired. |
| `Explain(ctx) string` | Human-readable multi-line report combining `Chain`, `Binding` and `Cause`, for logs. |

`Timeout` carries `File`, `Line`, `Func` (the call site), `Duration`,
`Deadline` (absolute), and `Created`.

## Overhead

Measured with `go test -bench . -benchmem`, Go 1.27.1, Apple M5 Pro
(`go test -run '^$' -bench . -benchmem -count=1 ./...`, pasted verbatim):

```
BenchmarkStdlibWithTimeout-15          7204670    147.4 ns/op    272 B/op    4 allocs/op
BenchmarkCtxwhyWithTimeout-15          2831502    423.9 ns/op    776 B/op    9 allocs/op
BenchmarkStdlibWithTimeoutNested-15    2504708    479.3 ns/op   1200 B/op   14 allocs/op
BenchmarkCtxwhyWithTimeoutNested-15     665181   1863   ns/op   2712 B/op   29 allocs/op
BenchmarkChain-15                     12227294    112.8 ns/op    672 B/op    3 allocs/op
```

Honestly: this is **not free**. A single `WithTimeout` is about 2.9x slower
and more than double the allocations of the stdlib call (the extra cost is
`runtime.Caller`/`FuncForPC` plus one small heap-allocated node and cause).
Three levels deep it's roughly 3.9x slower with 2x the allocations. In
absolute terms it's still sub-microsecond and almost certainly dwarfed by
whatever I/O the timeout is guarding — but if you're creating these in a
tight, allocation-sensitive hot loop rather than once per request/attempt,
that multiplier is real and you should measure your own case rather than
trust this number.

## What it does not do

- **It cannot see timeouts it didn't create.** If a plain `context.WithTimeout`
  (or any other library's context) is spliced into the middle of the chain,
  `Chain`/`Binding`/`Explain` silently skip it — they only know about nodes
  created via `ctxwhy.WithTimeout`/`WithDeadline`. `Cause` is honest about
  this: it returns `ok = false` rather than guessing, when the deadline that
  actually fired wasn't one of ours (see `TestCause_FalseWhenChainMixesPlainStdlibTimeout`).
- **It does not change cancellation semantics.** `ctx.Err()`, `Done()`, and
  `errors.Is(err, context.DeadlineExceeded)`/`context.Canceled` behave
  identically to stdlib. This is purely additive bookkeeping.
- **It is not a replacement for `context.WithCancel`/manual cancellation
  diagnostics.** `Cause` only reports something when a *deadline* fired; an
  explicit `cancel()` call correctly yields `ok = false`.
- **It does not detect misuse across goroutines that don't share the
  Context value** — if code reconstructs a fresh context instead of
  deriving from the one you were given, tracking is lost, same as any other
  context-value-based mechanism.
- **It has real, measured overhead** (see above) — this is a debugging aid
  for nested timeout chains, not a zero-cost abstraction.
- **No `Unwrap() []error` /  multi-cause fan-in support.** Only one cause
  chain (the one stdlib's `context.Cause` follows) is inspected.

## Develop

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .
```

## License

MIT
