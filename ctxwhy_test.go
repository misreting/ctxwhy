package ctxwhy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWithTimeout_CauseIdentifiesInnerWhenInnerFiresFirst(t *testing.T) {
	bg := context.Background()

	outer, cancelOuter := WithTimeout(bg, 500*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 10*time.Millisecond)
	defer cancelInner()

	<-inner.Done()

	got, ok := Cause(inner)
	if !ok {
		t.Fatalf("Cause: ok = false, want true")
	}
	if got.Duration != 10*time.Millisecond {
		t.Errorf("Cause().Duration = %v, want 10ms (the inner timeout, which fired first)", got.Duration)
	}
	if !strings.Contains(got.Func, "TestWithTimeout_CauseIdentifiesInnerWhenInnerFiresFirst") {
		t.Errorf("Cause().Func = %q, want it to name this test function", got.Func)
	}
	if !errors.Is(inner.Err(), context.DeadlineExceeded) {
		t.Errorf("inner.Err() = %v, want context.DeadlineExceeded (via errors.Is)", inner.Err())
	}
	if !errors.Is(context.Cause(inner), context.DeadlineExceeded) {
		t.Errorf("context.Cause(inner) = %v, want it to satisfy errors.Is(_, context.DeadlineExceeded)", context.Cause(inner))
	}
}

func TestWithTimeout_CauseIdentifiesOuterWhenOuterFiresFirst(t *testing.T) {
	bg := context.Background()

	// The reproduction from the task: an outer budget tighter than an
	// inner per-attempt timeout that looks generous but can never win.
	outer, cancelOuter := WithTimeout(bg, 10*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 500*time.Millisecond)
	defer cancelInner()

	<-inner.Done()

	got, ok := Cause(inner)
	if !ok {
		t.Fatalf("Cause: ok = false, want true")
	}
	if got.Duration != 10*time.Millisecond {
		t.Errorf("Cause().Duration = %v, want 10ms (the OUTER timeout, which fires first even though we ask Cause on the inner context)", got.Duration)
	}
}

func TestBinding_FlagsOuterAsBindingBeforeEitherFires(t *testing.T) {
	bg := context.Background()

	outer, cancelOuter := WithTimeout(bg, 50*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 500*time.Millisecond)
	defer cancelInner()

	// Neither has fired yet: this must be answerable pre-emptively.
	if inner.Err() != nil {
		t.Fatalf("precondition failed: inner already done")
	}

	binding, ok := Binding(inner)
	if !ok {
		t.Fatalf("Binding: ok = false, want true")
	}
	if binding.Duration != 50*time.Millisecond {
		t.Errorf("Binding().Duration = %v, want 50ms (the outer, tighter budget)", binding.Duration)
	}
}

func TestBinding_InnerWinsWhenItIsActuallyTighter(t *testing.T) {
	bg := context.Background()

	outer, cancelOuter := WithTimeout(bg, 500*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 50*time.Millisecond)
	defer cancelInner()

	binding, ok := Binding(inner)
	if !ok {
		t.Fatalf("Binding: ok = false, want true")
	}
	if binding.Duration != 50*time.Millisecond {
		t.Errorf("Binding().Duration = %v, want 50ms (the inner, tighter budget)", binding.Duration)
	}
}

func TestChain_OrdersOutermostFirst(t *testing.T) {
	bg := context.Background()

	a, cancelA := WithTimeout(bg, time.Hour)
	defer cancelA()
	b, cancelB := WithTimeout(a, time.Hour)
	defer cancelB()
	c, cancelC := WithTimeout(b, time.Hour)
	defer cancelC()

	chain := Chain(c)
	if len(chain) != 3 {
		t.Fatalf("len(Chain) = %d, want 3", len(chain))
	}
	for i, tt := range chain {
		if !strings.Contains(tt.Func, "TestChain_OrdersOutermostFirst") {
			t.Errorf("chain[%d].Func = %q, want this test function", i, tt.Func)
		}
	}
	// All three were created back-to-back with the same 1h budget, so
	// ordering can only be verified by Created timestamps increasing.
	if !chain[0].Created.Before(chain[1].Created) || !chain[1].Created.Before(chain[2].Created) {
		t.Errorf("Chain is not ordered outermost-first by creation time: %+v", chain)
	}
}

func TestChain_EmptyForUntrackedContext(t *testing.T) {
	bg := context.Background()
	if got := Chain(bg); got != nil {
		t.Errorf("Chain(background) = %v, want nil", got)
	}
	if _, ok := Binding(bg); ok {
		t.Errorf("Binding(background) ok = true, want false")
	}
}

func TestCause_FalseWhenChainMixesPlainStdlibTimeout(t *testing.T) {
	bg := context.Background()

	// A plain stdlib timeout tighter than our tracked one: it fires, but
	// ctxwhy never saw it, so Cause must honestly report ok=false rather
	// than fabricate an attribution.
	plain, cancelPlain := context.WithTimeout(bg, 5*time.Millisecond)
	defer cancelPlain()
	tracked, cancelTracked := WithTimeout(plain, time.Hour)
	defer cancelTracked()

	<-tracked.Done()

	if _, ok := Cause(tracked); ok {
		t.Errorf("Cause: ok = true, want false (the firing timeout was not created via ctxwhy)")
	}
	if !errors.Is(tracked.Err(), context.DeadlineExceeded) {
		t.Errorf("tracked.Err() = %v, want context.DeadlineExceeded", tracked.Err())
	}
}

func TestWithDeadline_Tracks(t *testing.T) {
	bg := context.Background()
	d := time.Now().Add(20 * time.Millisecond)
	ctx, cancel := WithDeadline(bg, d)
	defer cancel()

	chain := Chain(ctx)
	if len(chain) != 1 {
		t.Fatalf("len(Chain) = %d, want 1", len(chain))
	}
	if !chain[0].Deadline.Equal(d) {
		t.Errorf("chain[0].Deadline = %v, want %v", chain[0].Deadline, d)
	}

	<-ctx.Done()
	got, ok := Cause(ctx)
	if !ok {
		t.Fatalf("Cause: ok = false, want true")
	}
	if !got.Deadline.Equal(d) {
		t.Errorf("Cause().Deadline = %v, want %v", got.Deadline, d)
	}
}

func TestCancelBeforeDeadline_NoSpuriousCause(t *testing.T) {
	bg := context.Background()
	ctx, cancel := WithTimeout(bg, time.Hour)
	cancel()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
	if _, ok := Cause(ctx); ok {
		t.Errorf("Cause: ok = true after explicit cancel, want false (no deadline fired)")
	}
}

func TestExplain_ContainsCallSitesAndBindingMarker(t *testing.T) {
	bg := context.Background()
	outer, cancelOuter := WithTimeout(bg, 50*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 500*time.Millisecond)
	defer cancelInner()

	report := Explain(inner)
	if !strings.Contains(report, "BINDING") {
		t.Errorf("Explain report has no BINDING marker:\n%s", report)
	}
	if !strings.Contains(report, "shadowed") {
		t.Errorf("Explain report has no shadowed marker:\n%s", report)
	}
	if strings.Count(report, "ctxwhy_test.go") != 2 {
		t.Errorf("Explain report should name this test file for both entries:\n%s", report)
	}
}

func TestExplain_EmptyContext(t *testing.T) {
	got := Explain(context.Background())
	if !strings.Contains(got, "no tracked timeouts") {
		t.Errorf("Explain(background) = %q, want it to say there are no tracked timeouts", got)
	}
}

// TestConcurrentReads exercises Chain/Cause/Binding/Explain from many
// goroutines sharing the same derived contexts, for -race.
func TestConcurrentReads(t *testing.T) {
	bg := context.Background()
	outer, cancelOuter := WithTimeout(bg, 200*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := WithTimeout(outer, 20*time.Millisecond)
	defer cancelInner()

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				_ = Chain(inner)
				_, _ = Binding(inner)
				_, _ = Cause(inner)
				_ = Explain(inner)
			}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	<-inner.Done()
	if _, ok := Cause(inner); !ok {
		t.Errorf("Cause: ok = false after inner fired, want true")
	}
}
