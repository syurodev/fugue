package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationPackageBudgetDoesNotCancelNextPackage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	bin, err := toolPath(ctx, "gremlins")
	if err != nil {
		t.Fatal(err)
	}
	const fixture = "testdata/mutation-good"
	if _, timeoutErr := mutatePackage(ctx, fixture, bin, ".", -time.Second); !errors.Is(timeoutErr, context.DeadlineExceeded) {
		t.Fatalf("expired package budget: %v", timeoutErr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.Fatalf("package deadline canceled its caller: %v", ctxErr)
	}
	out, err := mutatePackage(ctx, fixture, bin, ".", time.Minute)
	if err != nil {
		t.Fatalf("next package did not get a fresh budget: %v", err)
	}
	if score, err := mutationScore(out); err != nil || score < mutationThreshold {
		t.Fatalf("next package was not fully checked: score=%v err=%v", score, err)
	}
}
