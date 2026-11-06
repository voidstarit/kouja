package lvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type flexString string

func (value *flexString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*value = ""
		return nil
	}
	if data[0] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		*value = flexString(decoded)
		return nil
	}
	*value = flexString(strings.TrimSpace(string(data)))
	return nil
}

func (value flexString) int64() (int64, error) {
	text := strings.TrimSpace(string(value))
	if text == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err == nil {
		return parsed, nil
	}
	floating, floatErr := strconv.ParseFloat(text, 64)
	if floatErr != nil {
		return 0, fmt.Errorf("%w: parse LVM number %q: %v", ErrInvalid, text, err)
	}
	return int64(floating), nil
}

type lvmReport struct {
	Report []lvmReportEntry `json:"report"`
}

type lvmReportEntry struct {
	VG []vgRow `json:"vg"`
	LV []lvRow `json:"lv"`
	PV []pvRow `json:"pv"`
}

type vgRow struct {
	Name           flexString `json:"vg_name"`
	UUID           flexString `json:"vg_uuid"`
	Attr           flexString `json:"vg_attr"`
	Size           flexString `json:"vg_size"`
	Free           flexString `json:"vg_free"`
	ExtentSize     flexString `json:"vg_extent_size"`
	MissingPVCount flexString `json:"vg_missing_pv_count"`
}

type lvRow struct {
	Name    flexString `json:"lv_name"`
	VGName  flexString `json:"vg_name"`
	UUID    flexString `json:"lv_uuid"`
	Size    flexString `json:"lv_size"`
	Attr    flexString `json:"lv_attr"`
	Tags    flexString `json:"lv_tags"`
	DMPath  flexString `json:"lv_dm_path"`
	Path    flexString `json:"lv_path"`
	Devices flexString `json:"devices"`
}

type pvRow struct {
	Name    flexString `json:"pv_name"`
	VGName  flexString `json:"vg_name"`
	UUID    flexString `json:"pv_uuid"`
	Attr    flexString `json:"pv_attr"`
	Missing flexString `json:"pv_missing"`
}

const vgColumns = "vg_name,vg_uuid,vg_attr,vg_size,vg_free,vg_extent_size,vg_missing_pv_count"
const lvColumns = "lv_name,vg_name,lv_uuid,lv_size,lv_attr,lv_tags,lv_dm_path,lv_path,devices"

func (backend *commandBackend) loadVGs(ctx context.Context) ([]vgRow, error) {
	output, err := backend.run(ctx, "vgs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", vgColumns)
	if err != nil {
		return nil, err
	}
	report, err := parseReport(output)
	if err != nil {
		return nil, err
	}
	rows := make([]vgRow, 0)
	for _, entry := range report.Report {
		rows = append(rows, entry.VG...)
	}
	return rows, nil
}

func (backend *commandBackend) loadLVs(ctx context.Context, target string) ([]lvRow, error) {
	args := []string{"--reportformat", "json", "--units", "b", "--nosuffix", "-o", lvColumns}
	if target != "" {
		args = append(args, target)
	}
	output, err := backend.run(ctx, "lvs", args...)
	if err != nil {
		return nil, err
	}
	report, err := parseReport(output)
	if err != nil {
		return nil, err
	}
	rows := make([]lvRow, 0)
	for _, entry := range report.Report {
		rows = append(rows, entry.LV...)
	}
	return rows, nil
}

func parseReport(output []byte) (lvmReport, error) {
	var report lvmReport
	if len(strings.TrimSpace(string(output))) == 0 {
		return report, nil
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return lvmReport{}, fmt.Errorf("%w: parse LVM JSON: %v", ErrInvalid, err)
	}
	return report, nil
}

func parseTags(raw string) map[string]string {
	tags := make(map[string]string)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key, value, found := strings.Cut(token, "=")
		if !found {
			tags[key] = ""
			continue
		}
		tags[key] = value
	}
	return tags
}

func rowToLV(row lvRow) (LV, error) {
	size, err := row.Size.int64()
	if err != nil {
		return LV{}, err
	}
	devicePath := string(row.DMPath)
	if devicePath == "" {
		devicePath = string(row.Path)
	}
	if devicePath == "" {
		devicePath = "/dev/" + string(row.VGName) + "/" + string(row.Name)
	}
	return LV{
		VG:         string(row.VGName),
		Name:       string(row.Name),
		UUID:       string(row.UUID),
		SizeBytes:  size,
		Tags:       parseTags(string(row.Tags)),
		DevicePath: devicePath,
		Attr:       string(row.Attr),
	}, nil
}

func (backend *commandBackend) commandDeviceProbe(ctx context.Context, vg, lv string) (string, int64, error) {
	candidates := []string{
		backend.devRoot + "/" + vg + "/" + lv,
		"/dev/mapper/" + vg + "-" + lv,
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		output, err := backend.run(ctx, "blockdev", "--getsize64", path)
		if err != nil {
			return "", 0, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
		if err != nil {
			return "", 0, fmt.Errorf("%w: parse blockdev size: %v", ErrInvalid, err)
		}
		return path, size, nil
	}
	return "", 0, ErrNotFound
}
