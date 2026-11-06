package csi

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidHandle = errors.New("csi: invalid volume handle")

type VolumeHandle struct {
	HostUUID     string
	ResourceUUID string
}

func ParseVolumeHandle(value string) (VolumeHandle, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" {
		return VolumeHandle{}, fmt.Errorf("%w: %q", ErrInvalidHandle, value)
	}
	return VolumeHandle{HostUUID: parts[1], ResourceUUID: parts[2]}, nil
}

func FormatVolumeHandle(hostUUID, resourceUUID string) string {
	return "v1:" + hostUUID + ":" + resourceUUID
}
