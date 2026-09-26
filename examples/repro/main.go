// Command repro reproduces the exact motivating scenario: a middleware-style
// overall budget (50ms) wrapping a per-attempt timeout that looks generous
// (500ms) but can never win. It first shows what plain context.Cause says,
// then what ctxwhy says about the same shape, before and after it fires.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/__GH_OWNER__/ctxwhy"
)

func main() {
	fmt.Println("=== plain context.WithTimeout + context.Cause ===")
	plainOuter, cancelPlainOuter := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelPlainOuter()
	plainInner, cancelPlainInner := context.WithTimeout(plainOuter, 500*time.Millisecond)
	defer cancelPlainInner()
	<-plainInner.Done()
	fmt.Printf("operation failed: %v (cause: %v)\n\n", plainInner.Err(), context.Cause(plainInner))

	fmt.Println("=== ctxwhy.WithTimeout, same shape, diagnosed BEFORE it fires ===")
	outer, cancelOuter := ctxwhy.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelOuter()
	inner, cancelInner := ctxwhy.WithTimeout(outer, 500*time.Millisecond)
	defer cancelInner()

	fmt.Print(ctxwhy.Explain(inner))
	fmt.Println()

	fmt.Println("=== same context, after it fires ===")
	<-inner.Done()
	fmt.Print(ctxwhy.Explain(inner))
}
