package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

var (
	ErrVGInUse        = errors.New("agent: volume group is in use")
	ErrVGUnavailable  = errors.New("agent: volume group is unavailable")
	ErrVGConflict     = errors.New("agent: volume group already exists with a different UUID")
	ErrVGUUIDMismatch = errors.New("agent: volume group UUID does not match")
	ErrVGNotFound     = errors.New("agent: volume group not found")
	ErrVGClassMapped  = errors.New("agent: volume group is referenced by a storage class")
)

type VGService interface {
	Register(ctx context.Context, name, expectedUUID string, replace bool) (lvm.Capacity, error)
	RegisterWithAudit(ctx context.Context, name, expectedUUID string, replace bool, event *state.AuditEvent) (lvm.Capacity, error)
	Deregister(ctx context.Context, name string) error
	DeregisterWithAudit(ctx context.Context, name string, event *state.AuditEvent) error
	Get(ctx context.Context, name string) (state.VG, error)
	List(ctx context.Context) ([]state.VG, error)
	Inspect(ctx context.Context, name string) (lvm.Capacity, error)
}

type VGManager struct {
	store   *state.Store
	lvm     lvm.Backend
	classes ClassRegistry
}

var _ VGService = (*VGManager)(nil)

func NewVGManager(store *state.Store, lvmBackend lvm.Backend, classes ClassRegistry) *VGManager {
	return &VGManager{store: store, lvm: lvmBackend, classes: classes}
}

func (manager *VGManager) Inspect(ctx context.Context, name string) (lvm.Capacity, error) {
	if manager == nil || manager.lvm == nil {
		return lvm.Capacity{}, fmt.Errorf("%w: LVM backend is not configured", ErrVGUnavailable)
	}
	if !vgNamePattern.MatchString(name) {
		return lvm.Capacity{}, fmt.Errorf("%w: invalid VG name %q", ErrVGUnavailable, name)
	}
	if err := manager.lvm.Eligibility(ctx, name); err != nil {
		return lvm.Capacity{}, fmt.Errorf("%w: %v", ErrVGUnavailable, err)
	}
	capacity, err := manager.lvm.Capacity(ctx, name)
	if err != nil {
		return lvm.Capacity{}, fmt.Errorf("%w: %v", ErrVGUnavailable, err)
	}
	return capacity, nil
}

func (manager *VGManager) Register(ctx context.Context, name, expectedUUID string, replace bool) (lvm.Capacity, error) {
	return manager.register(ctx, name, expectedUUID, replace, nil)
}

func (manager *VGManager) RegisterWithAudit(ctx context.Context, name, expectedUUID string, replace bool, event *state.AuditEvent) (lvm.Capacity, error) {
	return manager.register(ctx, name, expectedUUID, replace, event)
}

func (manager *VGManager) register(ctx context.Context, name, expectedUUID string, replace bool, event *state.AuditEvent) (lvm.Capacity, error) {
	capacity, err := manager.Inspect(ctx, name)
	if err != nil {
		return lvm.Capacity{}, err
	}
	if expectedUUID != "" && expectedUUID != capacity.VGUUID {
		return lvm.Capacity{}, fmt.Errorf("%w: expected %q, observed %q", ErrVGUUIDMismatch, expectedUUID, capacity.VGUUID)
	}
	now := time.Now().UTC()
	err = manager.store.Transact(ctx, func(tx state.Tx) error {
		existing, getErr := tx.GetVG(name)
		switch {
		case errors.Is(getErr, state.ErrNotFound):
			if err := tx.PutVG(state.VG{
				Name: name, UUID: capacity.VGUUID, State: "active",
				RegisteredAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
		case getErr != nil:
			return getErr
		case existing.UUID == capacity.VGUUID:
			existing.State = "active"
			existing.UpdatedAt = now
			if err := tx.PutVG(existing); err != nil {
				return err
			}
		case !replace:
			return fmt.Errorf("%w: existing UUID %q, observed %q", ErrVGConflict, existing.UUID, capacity.VGUUID)
		default:
			existing.UUID = capacity.VGUUID
			existing.State = "active"
			existing.UpdatedAt = now
			if err := tx.PutVG(existing); err != nil {
				return err
			}
		}
		if event != nil {
			if err := tx.PutAuditEvent(*event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return lvm.Capacity{}, err
	}
	return capacity, nil
}

func (manager *VGManager) Deregister(ctx context.Context, name string) error {
	return manager.deregister(ctx, name, nil)
}

func (manager *VGManager) DeregisterWithAudit(ctx context.Context, name string, event *state.AuditEvent) error {
	return manager.deregister(ctx, name, event)
}

func (manager *VGManager) deregister(ctx context.Context, name string, event *state.AuditEvent) error {
	if name == "" {
		return fmt.Errorf("%w: VG name is required", ErrVGNotFound)
	}
	if manager.classes != nil {
		for class, vg := range manager.classes.Snapshot() {
			if vg == name {
				return fmt.Errorf("%w: class %q", ErrVGClassMapped, class)
			}
		}
	}
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.DeleteVG(name); err != nil {
			return err
		}
		if event != nil {
			return tx.PutAuditEvent(*event)
		}
		return nil
	})
	if errors.Is(err, state.ErrConflict) {
		return fmt.Errorf("%w: %v", ErrVGInUse, err)
	}
	if errors.Is(err, state.ErrNotFound) {
		return ErrVGNotFound
	}
	return err
}

func (manager *VGManager) List(ctx context.Context) ([]state.VG, error) {
	var vgs []state.VG
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	})
	return vgs, err
}

func (manager *VGManager) Get(ctx context.Context, name string) (state.VG, error) {
	if name == "" {
		return state.VG{}, ErrVGNotFound
	}
	var vg state.VG
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetVG(name)
		vg = value
		return err
	})
	return vg, err
}
