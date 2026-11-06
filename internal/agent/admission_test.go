package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func bddTestAdmissionConcurrencyAndPerCaller(t testing.TB) {
	admission := NewAdmission(2, 1, 10)
	releaseOne, err := admission.AcquireOperation(context.Background(), "caller-a")
	if err != nil {
		t.Fatalf("first caller: %v", err)
	}
	if _, err := admission.AcquireOperation(context.Background(), "caller-a"); !errors.Is(err, ErrAdmission) {
		t.Fatalf("second caller-a = %v, want ErrAdmission", err)
	}
	releaseTwo, err := admission.AcquireOperation(context.Background(), "caller-b")
	if err != nil {
		t.Fatalf("caller-b: %v", err)
	}
	if _, err := admission.AcquireOperation(context.Background(), "caller-c"); !errors.Is(err, ErrAdmission) {
		t.Fatalf("global concurrency = %v, want ErrAdmission", err)
	}
	releaseOne()
	releaseOne()
	releaseTwo()
	release, err := admission.AcquireOperation(context.Background(), "caller-a")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release()
}

func bddTestAdmissionCapacityRate(t testing.TB) {
	admission := NewAdmission(2, 2, 1)
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	admission.now = func() time.Time { return base }
	admission.capacityAt = base
	admission.capacityTokens = 1
	if err := admission.AcquireCapacity(); err != nil {
		t.Fatalf("first capacity: %v", err)
	}
	if err := admission.AcquireCapacity(); !errors.Is(err, ErrAdmission) {
		t.Fatalf("second capacity = %v, want ErrAdmission", err)
	}
	admission.now = func() time.Time { return base.Add(time.Second) }
	if err := admission.AcquireCapacity(); err != nil {
		t.Fatalf("capacity after refill: %v", err)
	}
	if status.Code(statusError(ErrAdmission)) != codes.ResourceExhausted {
		t.Fatalf("status for admission = %v", status.Code(statusError(ErrAdmission)))
	}
}
