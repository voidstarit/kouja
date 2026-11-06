package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

const vgsSeededSetting = "vgs_seeded"

// InitializeVGs seeds configured VGs only on the first start and then verifies
// every registered VG against the host. A missing or changed VG marks its
// volumes as drift; it is never repaired automatically.
func InitializeVGs(ctx context.Context, store *state.Store, backend lvm.Backend, configured []string) error {
	if store == nil || backend == nil {
		return errors.New("agent: state store and LVM backend are required")
	}
	if err := seedConfiguredVGs(ctx, store, backend, configured); err != nil {
		return err
	}
	return VerifyVGs(ctx, store, backend)
}

func seedConfiguredVGs(ctx context.Context, store *state.Store, backend lvm.Backend, configured []string) error {
	seeded := false
	if err := store.Transact(ctx, func(tx state.Tx) error {
		_, err := tx.GetSetting(vgsSeededSetting)
		if err == nil {
			seeded = true
			return nil
		}
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		return err
	}); err != nil {
		return err
	}
	if seeded {
		return nil
	}
	now := time.Now().UTC()
	for _, name := range configured {
		capacity, err := backend.Capacity(ctx, name)
		if err != nil {
			slog.Warn("configured VG is unavailable during first-start seed", "vg", name, "error", err)
			continue
		}
		if err := backend.Eligibility(ctx, name); err != nil {
			slog.Warn("configured VG is not eligible during first-start seed", "vg", name, "error", err)
			continue
		}
		if err := store.Transact(ctx, func(tx state.Tx) error {
			return tx.PutVG(state.VG{
				Name: name, UUID: capacity.VGUUID, State: "active",
				RegisteredAt: now, UpdatedAt: now,
			})
		}); err != nil {
			return err
		}
	}
	return store.Transact(ctx, func(tx state.Tx) error {
		return tx.PutSetting(vgsSeededSetting, "true")
	})
}

func VerifyVGs(ctx context.Context, store *state.Store, backend lvm.Backend) error {
	var vgs []state.VG
	if err := store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, vg := range vgs {
		capacity, err := backend.Capacity(ctx, vg.Name)
		stateValue := "active"
		if err != nil || capacity.VGUUID != vg.UUID || capacity.VGState != "active" {
			stateValue = "unavailable"
		}
		if vg.State != stateValue {
			vg.State = stateValue
			vg.UpdatedAt = now
			if err := store.Transact(ctx, func(tx state.Tx) error {
				return tx.PutVG(vg)
			}); err != nil {
				return err
			}
		}
		if stateValue == "unavailable" {
			if err := markVGDependentVolumesDrift(ctx, store, vg.Name, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func markVGDependentVolumesDrift(ctx context.Context, store *state.Store, vgName string, now time.Time) error {
	cursor := ""
	for {
		var volumes []state.Volume
		if err := store.Transact(ctx, func(tx state.Tx) error {
			values, err := tx.ListVolumes("", state.Page{Limit: 100, Cursor: cursor})
			volumes = values
			return err
		}); err != nil {
			return err
		}
		if len(volumes) == 0 {
			return nil
		}
		for _, volume := range volumes {
			if volume.VGName != vgName {
				continue
			}
			if !containsString(volume.DriftReasons, "vg_unavailable") {
				volume.DriftReasons = append(volume.DriftReasons, "vg_unavailable")
			}
			volume.State = "drift"
			volume.UpdatedAt = now
			if err := store.Transact(ctx, func(tx state.Tx) error {
				return tx.PutVolume(volume)
			}); err != nil {
				return err
			}
		}
		cursor = volumes[len(volumes)-1].VolumeID
		if len(volumes) < 100 {
			return nil
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
