package ctxwhy

import (
	"context"
	"testing"
	"time"
)

// BenchmarkStdlibWithTimeout is the baseline: plain context.WithTimeout,
// immediately canceled, once per iteration.
func BenchmarkStdlibWithTimeout(b *testing.B) {
	bg := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, cancel := context.WithTimeout(bg, time.Hour)
		cancel()
	}
}

// BenchmarkCtxwhyWithTimeout is the same, through ctxwhy.WithTimeout, to
// measure the cost of call-site capture and chain bookkeeping.
func BenchmarkCtxwhyWithTimeout(b *testing.B) {
	bg := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, cancel := WithTimeout(bg, time.Hour)
		cancel()
	}
}

// BenchmarkStdlibWithTimeoutNested and BenchmarkCtxwhyWithTimeoutNested
// repeat the above three levels deep, which is the realistic shape (a
// middleware budget plus a couple of per-attempt timeouts).
func BenchmarkStdlibWithTimeoutNested(b *testing.B) {
	bg := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a, cancelA := context.WithTimeout(bg, time.Hour)
		bctx, cancelB := context.WithTimeout(a, time.Hour)
		c, cancelC := context.WithTimeout(bctx, time.Hour)
		cancelC()
		cancelB()
		cancelA()
		_ = c
	}
}

func BenchmarkCtxwhyWithTimeoutNested(b *testing.B) {
	bg := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a, cancelA := WithTimeout(bg, time.Hour)
		bctx, cancelB := WithTimeout(a, time.Hour)
		c, cancelC := WithTimeout(bctx, time.Hour)
		cancelC()
		cancelB()
		cancelA()
		_ = c
	}
}

// BenchmarkChain measures the cost of walking a 3-deep tracked chain, which
// Cause, Binding and Explain all do.
func BenchmarkChain(b *testing.B) {
	bg := context.Background()
	a, cancelA := WithTimeout(bg, time.Hour)
	defer cancelA()
	bctx, cancelB := WithTimeout(a, time.Hour)
	defer cancelB()
	c, cancelC := WithTimeout(bctx, time.Hour)
	defer cancelC()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Chain(c)
	}
}
