package backup

import "testing"

func TestIsBackupObject(t *testing.T) {
	folder := []struct {
		key  string
		want bool
	}{
		{"kouja/2026Nov091200/state.db", true},
		{"kouja/2026Nov091200/restore.json.enc", true},
		{"kouja/2026Nov091200/certificates/admin.crt", true},
		{"kouja/2026Nov/state.db", false},
		{"kouja/other/state.db", false},
		{"other/2026Nov091200/state.db", false},
	}
	for _, test := range folder {
		if got := isBackupObject(test.key, "kouja/", true); got != test.want {
			t.Errorf("isBackupObject(%q, folder) = %v, want %v", test.key, got, test.want)
		}
	}
	flat := []struct {
		key  string
		want bool
	}{
		{"kouja/kouja-20261109T120000.000000000Z-state.db", true},
		{"kouja/kouja-20261109T120000.000000000Z-restore.json.enc", true},
		{"kouja/kouja-20261109T120000.000000000Z-certificates-admin.crt", true},
		{"kouja/random", false},
		{"kouja/kouja-not-a-timestamp-state.db", false},
		{"other/kouja-20261109T120000.000000000Z-state.db", false},
	}
	for _, test := range flat {
		if got := isBackupObject(test.key, "kouja/", false); got != test.want {
			t.Errorf("isBackupObject(%q, flat) = %v, want %v", test.key, got, test.want)
		}
	}
}

func TestRemoteObjectNamesAndSafety(t *testing.T) {
	if got := remoteObjectName("certificates/admin.crt", "base", true); got != "certificates/admin.crt" {
		t.Fatalf("folder name = %q", got)
	}
	if got := remoteObjectName("certificates/admin.crt", "base", false); got != "base-certificates-admin.crt" {
		t.Fatalf("flat name = %q", got)
	}
	if _, err := safeBackupName("../secret"); err == nil {
		t.Fatal("unsafe name accepted")
	}
	if _, err := safeBackupName("/absolute"); err == nil {
		t.Fatal("absolute name accepted")
	}
	if got, err := safeBackupName("tls/server.crt"); err != nil || got != "tls/server.crt" {
		t.Fatalf("safe name = %q, %v", got, err)
	}
}
