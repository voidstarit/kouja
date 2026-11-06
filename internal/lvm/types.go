package lvm

import (
	"context"
	"time"
)

type LV struct {
	VG         string
	Name       string
	UUID       string
	SizeBytes  int64
	Tags       map[string]string
	DevicePath string
	Attr       string
}

type Capacity struct {
	VGName           string
	VGUUID           string
	VGState          string
	TotalBytes       int64
	FreeBytes        int64
	AllocatableBytes int64
	ExtentSizeBytes  int64
	MaxVolumeBytes   int64
	ObservedAt       time.Time
}

type Backend interface {
	Create(ctx context.Context, vg, lvName string, bytes int64, tags map[string]string) (LV, error)
	Extend(ctx context.Context, vg, lvName string, bytes int64) (LV, error)
	Shrink(ctx context.Context, vg, lvName string, bytes int64) (LV, error)
	Remove(ctx context.Context, vg, lvName string) error
	Activate(ctx context.Context, vg, lvName string) error
	Observe(ctx context.Context, vg, lvName string) (LV, error)
	ListManaged(ctx context.Context) ([]LV, error)
	ListCandidates(ctx context.Context) ([]LV, error)
	SetOwnerTag(ctx context.Context, vg, lvName, ownerHash string) error
	Capacity(ctx context.Context, vg string) (Capacity, error)
	Eligibility(ctx context.Context, vg string) error
}
