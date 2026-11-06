package csi

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func bddTestKubernetesPlacementLifecycle(t testing.TB) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{placementResource: "KoujaVolumePlacementList"},
	)
	placement := &KubernetesPlacement{
		client: client,
		now:    func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
	}
	ctx := context.Background()
	key := "pvc-123"
	if _, err := placement.Get(ctx, key); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("missing placement: %v", err)
	}
	if err := placement.Pending(ctx, key, Host{UUID: "host-1", Endpoint: "10.0.0.1:9443"}, key); err != nil {
		t.Fatalf("pending: %v", err)
	}
	record, err := placement.Get(ctx, key)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if record.HostUUID != "host-1" || record.VolumeID != key || record.Phase != "Pending" {
		t.Fatalf("pending record: %#v", record)
	}
	if err := placement.Bind(ctx, key, "resource-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	record, err = placement.Get(ctx, key)
	if err != nil {
		t.Fatalf("get bound: %v", err)
	}
	if record.ResourceUUID != "resource-1" || record.Phase != "Bound" {
		t.Fatalf("bound record: %#v", record)
	}
	records, err := placement.List(ctx)
	if err != nil || len(records) != 1 || records[0].Key != key {
		t.Fatalf("list = %#v, %v", records, err)
	}
	if err := placement.Fail(ctx, key, errors.New("boom")); err != nil {
		t.Fatalf("fail: %v", err)
	}
	record, err = placement.Get(ctx, key)
	if err != nil || record.Phase != "Failed" || record.Error != "boom" {
		t.Fatalf("failed record: %#v, %v", record, err)
	}
	if err := placement.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := placement.Get(ctx, key); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
