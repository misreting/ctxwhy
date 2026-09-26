// Package ctxwhy answers one question that plain context.Context cannot:
// when a chain of nested timeouts finally fires, WHICH one fired, where in
// the source was it created, and what was its budget?
//
// context.Cause (Go 1.21+) tells you the cause an ancestor was canceled
// with, but only if every WithTimeout/WithDeadline call site in the chain
// remembered to pass a distinct, descriptive cause via WithTimeoutCause. In
// practice almost nobody does that consistently, so context.Cause(ctx) on a
// chain built from plain context.WithTimeout calls returns the same
// context.DeadlineExceeded sentinel no matter which of N nested timeouts
// actually expired.
//
// ctxwhy.WithTimeout and ctxwhy.WithDeadline are drop-in replacements for
// their stdlib counterparts that automatically record the call site
// (file:line and function name) and the budget at creation time, so that
// later, given only the (possibly deeply nested) resulting Context, you can
// call Cause to find out which node fired, or Chain/Explain to see the
// whole nesting and — before anything ever fires — which node is the
// binding one and which are dead code.
package ctxwhy

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Timeout describes a single ctxwhy.WithTimeout or ctxwhy.WithDeadline call:
// where it was created, what its budget was, and the absolute time it
// expires.
type Timeout struct {
	// File and Line are the source location of the WithTimeout/WithDeadline
	// call, as reported by runtime.Caller.
	File string
	Line int
	// Func is the fully-qualified name of the function that made the call.
	Func string
	// Duration is the budget passed to WithTimeout, or the duration until
	// Deadline as of the WithDeadline call. It is informational only; the
	// authoritative value is Deadline.
	Duration time.Duration
	// Deadline is the absolute time this node expires.
	Deadline time.Time
	// Created is when this node was constructed.
	Created time.Time
}

// site formats the file:line/function of the Timeout for display.
func (t Timeout) site() string {
	file := t.File
	if idx := strings.LastIndexByte(file, '/'); idx >= 0 {
		file = file[idx+1:]
	}
	return fmt.Sprintf("%s:%d (%s)", file, t.Line, t.Func)
}

// String renders a one-line, human-readable description of the Timeout,
// e.g. "50ms timeout set at server.go:12 (main.handle), deadline 15:04:05.000".
func (t Timeout) String() string {
	return fmt.Sprintf("%s timeout set at %s, deadline %s", t.Duration, t.site(), t.Deadline.Format("15:04:05.000"))
}

// chainKey is the context key under which the current chain node is stored.
type chainKey struct{}

// node is one link in the linked list of Timeouts tracked for a Context. It
// is immutable once constructed, so sharing it across goroutines via a
// Context is race-free.
type node struct {
	t      Timeout
	parent *node
}

// timeoutCause is the error installed as the cancellation cause of the
// context.Context returned by WithTimeout/WithDeadline. It carries the full
// Timeout that created it, and unwraps to context.DeadlineExceeded so that
// errors.Is(err, context.DeadlineExceeded) keeps working exactly as it does
// for a plain stdlib timeout.
type timeoutCause struct {
	t Timeout
}

func (c *timeoutCause) Error() string {
	return fmt.Sprintf("context deadline exceeded: %s", c.t)
}

func (c *timeoutCause) Unwrap() error {
	return context.DeadlineExceeded
}

// WithTimeout is context.WithTimeout, plus bookkeeping: it records the file,
// line and function of this call site and the requested duration, so that
// Cause, Chain and Explain can later report on it. The returned CancelFunc
// behaves exactly like the one from context.WithTimeout.
func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return with(parent, time.Now().Add(timeout), timeout, 2)
}

// WithDeadline is context.WithDeadline, plus the same bookkeeping as
// WithTimeout.
func WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return with(parent, deadline, time.Until(deadline), 2)
}

func with(parent context.Context, deadline time.Time, duration time.Duration, skip int) (context.Context, context.CancelFunc) {
	file, line, fn := caller(skip + 1)
	t := Timeout{
		File:     file,
		Line:     line,
		Func:     fn,
		Duration: duration,
		Deadline: deadline,
		Created:  time.Now(),
	}
	cause := &timeoutCause{t: t}

	ctx, cancel := context.WithDeadlineCause(parent, deadline, cause)

	parentNode, _ := parent.Value(chainKey{}).(*node)
	ctx = context.WithValue(ctx, chainKey{}, &node{t: t, parent: parentNode})

	return ctx, cancel
}

// caller reports the file, line and function name of the caller `skip`
// frames up from caller() itself.
func caller(skip int) (file string, line int, fn string) {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown", 0, "unknown"
	}
	fn = "unknown"
	if f := runtime.FuncForPC(pc); f != nil {
		fn = f.Name()
		if idx := strings.LastIndexByte(fn, '/'); idx >= 0 {
			fn = fn[idx+1:]
		}
	}
	return file, line, fn
}

// Cause reports the Timeout that is responsible for ctx being canceled, if
// ctx (or one of its ancestors) was created by WithTimeout or WithDeadline
// and that specific node is the one whose deadline actually fired (or whose
// CancelFunc's cancellation propagated the firing cause down). ok is false
// if ctx is not yet done, or its cancellation cause did not originate from
// this package (e.g. a plain context.WithCancel further up the chain, or a
// deadline created with plain context.WithTimeout).
func Cause(ctx context.Context) (timeout Timeout, ok bool) {
	var tc *timeoutCause
	if errors.As(context.Cause(ctx), &tc) {
		return tc.t, true
	}
	return Timeout{}, false
}

// Chain returns every Timeout tracked on ctx, from the outermost
// (root-most) call to the innermost, in the order they were nested. It only
// sees timeouts created via this package's WithTimeout/WithDeadline; a
// plain context.WithTimeout spliced into the chain is invisible to it (see
// the package README's "What it does not do").
func Chain(ctx context.Context) []Timeout {
	n, _ := ctx.Value(chainKey{}).(*node)
	var out []Timeout
	for cur := n; cur != nil; cur = cur.parent {
		out = append(out, cur.t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Binding returns the Timeout in ctx's tracked chain with the earliest
// absolute deadline — the one that will actually fire (or did fire) unless
// something cancels the context first. Every other tracked Timeout with a
// later deadline is, in effect, dead code: it can never be the reason ctx's
// Done channel closes, because an ancestor with an earlier deadline cancels
// the whole subtree first. ok is false if ctx has no tracked timeouts.
//
// This can be called before any deadline has fired, which is the point: it
// lets you catch "this per-attempt timeout is longer than the budget its
// caller already imposed" as a static-ish check instead of a confusing
// runtime symptom.
func Binding(ctx context.Context) (timeout Timeout, ok bool) {
	chain := Chain(ctx)
	if len(chain) == 0 {
		return Timeout{}, false
	}
	best := chain[0]
	for _, t := range chain[1:] {
		if t.Deadline.Before(best.Deadline) {
			best = t
		}
	}
	return best, true
}

// Explain renders a human-readable report of every tracked timeout on ctx:
// each call site, its budget, whether it is the binding (effective) one or
// is shadowed by an earlier ancestor deadline, and — if ctx is already
// done — which node's deadline actually fired. It is meant for logs and
// debugging output, not for programmatic decisions; use Chain and Binding
// for those.
func Explain(ctx context.Context) string {
	chain := Chain(ctx)
	if len(chain) == 0 {
		return "ctxwhy: no tracked timeouts on this context"
	}

	binding, _ := Binding(ctx)

	var b strings.Builder
	fmt.Fprintf(&b, "ctxwhy: %d tracked timeout(s), outermost first:\n", len(chain))
	for _, t := range chain {
		marker := "shadowed"
		if t == binding {
			marker = "BINDING — this one wins"
		}
		fmt.Fprintf(&b, "  - %s [%s]\n", t, marker)
	}

	if fired, ok := Cause(ctx); ok {
		fmt.Fprintf(&b, "fired: %s\n", fired)
	} else if ctx.Err() != nil {
		fmt.Fprintf(&b, "context is done (%v) but the cause was not set by ctxwhy\n", ctx.Err())
	}

	return b.String()
}
