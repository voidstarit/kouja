package lvm

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func validateSize(bytes int64) error {
	if bytes <= 0 {
		return fmt.Errorf("%w: size must be greater than zero", ErrInvalid)
	}
	if bytes > maxVolumeBytes {
		return fmt.Errorf("%w: size %d exceeds the 1 TiB limit", ErrInvalid, bytes)
	}
	return nil
}

func (backend *commandBackend) Create(ctx context.Context, vg, lvName string, bytes int64, tags map[string]string) (LV, error) {
	if err := validateVolumeName(vg, lvName); err != nil {
		return LV{}, err
	}
	if err := validateSize(bytes); err != nil {
		return LV{}, err
	}
	args := []string{"--yes", "--size", fmt.Sprintf("%db", bytes), "--name", lvName}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		if key == "" {
			return LV{}, fmt.Errorf("%w: empty LVM tag key", ErrInvalid)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--addtag", key+"="+tags[key])
	}
	args = append(args, vg)
	if _, err := backend.run(ctx, "lvcreate", args...); err != nil {
		return LV{}, err
	}
	if err := backend.settle(ctx); err != nil {
		return LV{}, err
	}
	if _, err := backend.waitForDevice(ctx, vg, lvName, bytes); err != nil {
		return LV{}, err
	}
	return backend.Observe(ctx, vg, lvName)
}

func (backend *commandBackend) Extend(ctx context.Context, vg, lvName string, bytes int64) (LV, error) {
	if err := validateVolumeName(vg, lvName); err != nil {
		return LV{}, err
	}
	if err := validateSize(bytes); err != nil {
		return LV{}, err
	}
	if _, err := backend.run(ctx, "lvextend", "--yes", "--size", fmt.Sprintf("%db", bytes), vg+"/"+lvName); err != nil {
		return LV{}, err
	}
	if err := backend.settle(ctx); err != nil {
		return LV{}, err
	}
	if _, err := backend.waitForDevice(ctx, vg, lvName, bytes); err != nil {
		return LV{}, err
	}
	return backend.Observe(ctx, vg, lvName)
}

func (backend *commandBackend) Shrink(ctx context.Context, vg, lvName string, bytes int64) (LV, error) {
	if err := validateVolumeName(vg, lvName); err != nil {
		return LV{}, err
	}
	if err := validateSize(bytes); err != nil {
		return LV{}, err
	}
	if _, err := backend.run(ctx, "lvreduce", "--yes", "--size", fmt.Sprintf("%db", bytes), vg+"/"+lvName); err != nil {
		return LV{}, err
	}
	if err := backend.settle(ctx); err != nil {
		return LV{}, err
	}
	if _, err := backend.waitForDevice(ctx, vg, lvName, bytes); err != nil {
		return LV{}, err
	}
	return backend.Observe(ctx, vg, lvName)
}

func (backend *commandBackend) Remove(ctx context.Context, vg, lvName string) error {
	if err := validateVolumeName(vg, lvName); err != nil {
		return err
	}
	_, err := backend.run(ctx, "lvremove", "--yes", vg+"/"+lvName)
	return err
}

func (backend *commandBackend) Activate(ctx context.Context, vg, lvName string) error {
	if err := validateVolumeName(vg, lvName); err != nil {
		return err
	}
	if _, err := backend.run(ctx, "lvchange", "--activate", "y", vg+"/"+lvName); err != nil {
		return err
	}
	return backend.settle(ctx)
}

func (backend *commandBackend) Observe(ctx context.Context, vg, lvName string) (LV, error) {
	if err := validateVolumeName(vg, lvName); err != nil {
		return LV{}, err
	}
	rows, err := backend.loadLVs(ctx, vg+"/"+lvName)
	if err != nil {
		return LV{}, err
	}
	if len(rows) == 0 {
		return LV{}, ErrNotFound
	}
	volume, err := rowToLV(rows[0])
	if err != nil {
		return LV{}, err
	}
	return volume, nil
}

func (backend *commandBackend) ListManaged(ctx context.Context) ([]LV, error) {
	rows, err := backend.loadLVs(ctx, "")
	if err != nil {
		return nil, err
	}
	volumes := make([]LV, 0)
	for _, row := range rows {
		volume, err := rowToLV(row)
		if err != nil {
			return nil, err
		}
		if volume.Tags["kouja.managed"] == "true" {
			volumes = append(volumes, volume)
		}
	}
	return volumes, nil
}

func (backend *commandBackend) ListCandidates(ctx context.Context) ([]LV, error) {
	rows, err := backend.loadLVs(ctx, "")
	if err != nil {
		return nil, err
	}
	volumes := make([]LV, 0, len(rows))
	for _, row := range rows {
		volume, err := rowToLV(row)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volume)
	}
	return volumes, nil
}

func (backend *commandBackend) SetOwnerTag(ctx context.Context, vg, lvName, ownerHash string) error {
	if err := validateVolumeName(vg, lvName); err != nil {
		return err
	}
	if ownerHash == "" {
		return fmt.Errorf("%w: owner hash is required", ErrInvalid)
	}
	current, err := backend.Observe(ctx, vg, lvName)
	if err != nil {
		return err
	}
	oldTags := make([]string, 0)
	for key, value := range current.Tags {
		if key == "kouja.owner" {
			oldTags = append(oldTags, key+"="+value)
		}
	}
	sort.Strings(oldTags)
	args := make([]string, 0, len(oldTags)*2+3)
	for _, tag := range oldTags {
		args = append(args, "--deltag", tag)
	}
	args = append(args, "--addtag", "kouja.owner="+ownerHash, vg+"/"+lvName)
	_, err = backend.run(ctx, "lvchange", args...)
	return err
}

func (backend *commandBackend) Capacity(ctx context.Context, vg string) (Capacity, error) {
	if err := validateName("volume group", vg); err != nil {
		return Capacity{}, err
	}
	rows, err := backend.loadVGs(ctx)
	if err != nil {
		return Capacity{}, err
	}
	var row *vgRow
	for index := range rows {
		if string(rows[index].Name) == vg {
			row = &rows[index]
			break
		}
	}
	if row == nil {
		return Capacity{}, ErrNotFound
	}
	total, err := row.Size.int64()
	if err != nil {
		return Capacity{}, err
	}
	free, err := row.Free.int64()
	if err != nil {
		return Capacity{}, err
	}
	extentSize, err := row.ExtentSize.int64()
	if err != nil {
		return Capacity{}, err
	}
	missing, err := row.MissingPVCount.int64()
	if err != nil {
		return Capacity{}, err
	}
	percent := backend.headroomPercent
	if percent < 0 {
		percent = 0
	}
	headroom := total * percent / 100
	if backend.headroomMinBytes > headroom {
		headroom = backend.headroomMinBytes
	}
	allocatable := free - headroom
	if allocatable < 0 {
		allocatable = 0
	}
	maxVolume := allocatable
	if maxVolume > maxVolumeBytes {
		maxVolume = maxVolumeBytes
	}
	state := "active"
	if missing > 0 {
		state = "unavailable"
	}
	return Capacity{
		VGName:           vg,
		VGUUID:           string(row.UUID),
		VGState:          state,
		TotalBytes:       total,
		FreeBytes:        free,
		AllocatableBytes: allocatable,
		ExtentSizeBytes:  extentSize,
		MaxVolumeBytes:   maxVolume,
		ObservedAt:       time.Now().UTC(),
	}, nil
}

func (backend *commandBackend) Eligibility(ctx context.Context, vg string) error {
	if err := validateName("volume group", vg); err != nil {
		return err
	}
	vgRows, err := backend.loadVGs(ctx)
	if err != nil {
		return err
	}
	var candidate *vgRow
	for index := range vgRows {
		if string(vgRows[index].Name) == vg {
			candidate = &vgRows[index]
			break
		}
	}
	if candidate == nil {
		return ErrNotFound
	}
	missing, err := candidate.MissingPVCount.int64()
	if err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("%w: volume group %q has %d missing physical volumes", ErrIneligible, vg, missing)
	}
	attr := strings.ToLower(string(candidate.Attr))
	if strings.ContainsAny(attr, "cs") {
		return fmt.Errorf("%w: volume group %q is clustered or shared", ErrIneligible, vg)
	}

	lvRows, err := backend.loadLVs(ctx, vg)
	if err != nil {
		return err
	}
	for _, row := range lvRows {
		lvAttr := string(row.Attr)
		if len(lvAttr) > 0 && (lvAttr[0] == 't' || lvAttr[0] == 'T') {
			return fmt.Errorf("%w: volume group %q contains thin LV %q", ErrIneligible, vg, string(row.Name))
		}
	}

	for _, path := range backend.criticalPaths {
		output, err := backend.run(ctx, "findmnt", "-n", "-o", "SOURCE", "--target", path)
		if err != nil {
			return fmt.Errorf("lvm: inspect critical path %q: %w", path, err)
		}
		source := strings.TrimSpace(string(output))
		if source == "" {
			continue
		}
		for _, row := range lvRows {
			if string(row.DMPath) == source || string(row.Path) == source {
				return fmt.Errorf("%w: volume group %q backs critical path %q", ErrIneligible, vg, path)
			}
		}
	}
	return nil
}

func (backend *commandBackend) settle(ctx context.Context) error {
	if backend.settleTimeout < 0 {
		return nil
	}
	timeout := backend.settleTimeout
	if timeout <= 0 {
		timeout = defaultSettleTimeout
	}
	seconds := int64(timeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	_, err := backend.run(ctx, "udevadm", "settle", "--timeout="+strconv.FormatInt(seconds, 10))
	return err
}

func (backend *commandBackend) waitForDevice(ctx context.Context, vg, lvName string, expectedBytes int64) (string, error) {
	timeout := backend.settleTimeout
	if timeout <= 0 {
		timeout = defaultSettleTimeout
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		path, size, err := backend.probe(waitContext, vg, lvName)
		if err == nil && size >= expectedBytes {
			return path, nil
		}
		lastErr = err
		select {
		case <-waitContext.Done():
			if lastErr == nil {
				lastErr = waitContext.Err()
			}
			return "", fmt.Errorf("lvm: device %s/%s did not become ready: %w", vg, lvName, lastErr)
		case <-time.After(backend.pollInterval):
		}
	}
}
