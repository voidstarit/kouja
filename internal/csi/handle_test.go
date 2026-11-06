package csi

import "testing"

func TestVolumeHandleRoundTrip(t *testing.T) {
	handle := FormatVolumeHandle("host-1", "resource-1")
	if handle != "v1:host-1:resource-1" {
		t.Fatalf("handle = %q", handle)
	}
	parsed, err := ParseVolumeHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HostUUID != "host-1" || parsed.ResourceUUID != "resource-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestVolumeHandleRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "v1:host", "v2:host:resource", "v1::resource", "v1:host:", "v1:host:resource:extra"} {
		if _, err := ParseVolumeHandle(value); err == nil {
			t.Fatalf("ParseVolumeHandle(%q) succeeded", value)
		}
	}
}

func TestProtocolValue(t *testing.T) {
	tests := map[string]string{
		"iscsi":       "PROTOCOL_ISCSI",
		"nvme":        "PROTOCOL_NVME_TCP",
		"nvme-tcp":    "PROTOCOL_NVME_TCP",
		"nvme_tcp":    "PROTOCOL_NVME_TCP",
		"unspecified": "PROTOCOL_ISCSI",
	}
	for input, want := range tests {
		if got := protocolValue(input).String(); got != want {
			t.Errorf("protocolValue(%q) = %s, want %s", input, got, want)
		}
	}
}
