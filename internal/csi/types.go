package csi

import (
	"context"
	"time"
)

type Host struct {
	UUID       string
	Endpoint   string
	Portals    []string
	Ready      bool
	Protocols  []string
	ObservedAt time.Time
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

type SelectRequest struct {
	VG        string
	Protocol  string
	SizeBytes int64
}

type PlacementRecord struct {
	Key            string
	CSIVolumeName  string
	StorageClass   string
	HostUUID       string
	VolumeID       string
	ResourceUUID   string
	IdempotencyKey string
	Phase          string
	Error          string
	UpdatedAt      time.Time
}

type ConnectSpec struct {
	Protocol   string
	TargetName string
	Portal     string
	LUN        int32
	NSID       int32
	Initiator  string
	Credential string
}

type Device struct {
	Path      string
	SizeBytes int64
}

type Inventory interface {
	Resolve(ctx context.Context, srvRecord string) ([]Host, error)
	Capacity(ctx context.Context, host Host, vg string) (Capacity, error)
	Select(ctx context.Context, hosts []Host, request SelectRequest) (Host, error)
}

type Placement interface {
	Get(ctx context.Context, key string) (PlacementRecord, error)
	List(ctx context.Context) ([]PlacementRecord, error)
	Pending(ctx context.Context, key string, host Host, volumeID, storageClass string) error
	Bind(ctx context.Context, key string, resourceUUID string) error
	Fail(ctx context.Context, key string, err error) error
	Delete(ctx context.Context, key string) error
}

type Mounter interface {
	Connect(ctx context.Context, spec ConnectSpec) (Device, error)
	Disconnect(ctx context.Context, spec ConnectSpec) error
	Format(ctx context.Context, device Device, fsType string) error
	HasFilesystem(ctx context.Context, device Device) (bool, error)
	Mount(ctx context.Context, device Device, target string) error
	IsMounted(ctx context.Context, target string) (bool, error)
	Unmount(ctx context.Context, target string) error
	Resize(ctx context.Context, device Device, fsType string) error
}
