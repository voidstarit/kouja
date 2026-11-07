package target

import (
	"context"
	"errors"
	"os"
	"path"
	"testing"
)

func writeMetricFile(t *testing.T, fs *ConfigFS, rel, value string) {
	t.Helper()
	if err := fs.Mkdir(path.Dir(rel)); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(rel, value); err != nil {
		t.Fatal(err)
	}
}

func TestISCSIMetricsReadsConfigFSStats(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	if _, err := adapter.Export(context.Background(), spec, testCredential()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	targetPath := path.Join("target/iscsi", spec.TargetName)
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/sessions"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/nodes"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/portals"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_login_stats/accepts"), "3\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_login_stats/authenticate_fails"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_login_stats/authorize_fails"), "2\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_login_stats/negotiate_fails"), "3\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_login_stats/other_fails"), "4\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_tgt_attr/login_fails"), "10\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_logout_stats/abnormal_logouts"), "2\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_sess_err/format_errors"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_sess_err/cxn_errors"), "2\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_sess_err/digest_errors"), "3\n")
	aclPath := path.Join(targetPath, "tpgt_1/acls", spec.Initiator)
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/rxdata_octs"), "12582912\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/txdata_octs"), "1048576\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/cmd_pdus"), "42\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/rsp_pdus"), "40\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/conn_timeout_errors"), "4\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "fabric_statistics/iscsi_sess_stats/conn_digest_errors"), "5\n")
	stats := "target/core/iblock_0/lv1/statistics"
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/num_cmds"), "42\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/read_mbytes"), "3\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/write_mbytes"), "4\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/resets"), "6\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/status"), "available\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_lu/state_bit"), "exposed\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_tgt_dev/aborts_complete"), "7\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_tgt_dev/aborts_no_task"), "8\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(stats, "scsi_tgt_dev/resets"), "9\n")
	lunStats := path.Join(targetPath, "tpgt_1/lun/lun_0/statistics/scsi_tgt_port")
	writeMetricFile(t, fs.ConfigFS, path.Join(lunStats, "busy_count"), "10\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(lunStats, "in_cmds"), "11\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(lunStats, "hs_in_cmds"), "12\n")

	metrics, err := adapter.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(metrics.Targets) != 1 {
		t.Fatalf("targets = %#v", metrics.Targets)
	}
	target := metrics.Targets[0]
	if target.Target != spec.TargetName || target.Sessions != 1 || target.Nodes != 1 || target.Portals != 1 {
		t.Fatalf("target = %#v", target)
	}
	if target.Login.Accepts != 3 || target.Login.AuthenticateFails != 1 ||
		target.Login.AuthorizeFails != 2 || target.Login.NegotiateFails != 3 ||
		target.Login.OtherFails != 4 || target.Login.TargetLoginFails != 10 {
		t.Fatalf("login = %#v", target.Login)
	}
	if target.Logout.Abnormal != 2 || target.SessionErrors.FormatErrors != 1 ||
		target.SessionErrors.ConnectionErrors != 2 || target.SessionErrors.DigestErrors != 3 {
		t.Fatalf("target errors = %#v %#v", target.Logout, target.SessionErrors)
	}
	if len(target.Initiators) != 1 {
		t.Fatalf("initiators = %#v", target.Initiators)
	}
	initiator := target.Initiators[0]
	if initiator.Initiator != spec.Initiator || initiator.RXBytes != 12582912 ||
		initiator.TXBytes != 1048576 || initiator.CommandPDUs != 42 ||
		initiator.ResponsePDUs != 40 || initiator.ConnectionTimeoutErrors != 4 ||
		initiator.ConnectionDigestErrors != 5 {
		t.Fatalf("initiator = %#v", initiator)
	}
	if len(target.LUNs) != 1 {
		t.Fatalf("luns = %#v", target.LUNs)
	}
	lun := target.LUNs[0]
	if lun.LUN != 0 || lun.ResourceUUID != spec.ResourceUUID || lun.Commands != 42 ||
		lun.ReadBytes != 3*1024*1024 || lun.WriteBytes != 4*1024*1024 ||
		lun.Resets != 15 || lun.AbortsComplete != 7 || lun.AbortsNoTask != 8 ||
		lun.Status != "available" || lun.StateBit != "exposed" ||
		lun.BusyCount != 10 || lun.InCommands != 11 || lun.HighPriorityInCommands != 12 {
		t.Fatalf("lun = %#v", lun)
	}
}

func TestISCSIMetricsEmptyAndContext(t *testing.T) {
	adapter, _ := newTestISCSI(t)
	metrics, err := adapter.Metrics(context.Background())
	if err != nil || len(metrics.Targets) != 0 {
		t.Fatalf("empty metrics = %#v, %v", metrics, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Metrics(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled metrics = %v", err)
	}
}

func TestISCSIMetricsReportsBadTargetStats(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	if _, err := adapter.Export(context.Background(), spec, testCredential()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	targetPath := path.Join("target/iscsi", spec.TargetName)
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/sessions"), "not-a-number\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/nodes"), "1\n")
	writeMetricFile(t, fs.ConfigFS, path.Join(targetPath, "fabric_statistics/iscsi_instance/portals"), "1\n")
	metrics, err := adapter.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(metrics.Targets) != 1 || metrics.Targets[0].Error == "" {
		t.Fatalf("bad metrics = %#v", metrics)
	}
}

func TestISCSIClientConnectionsAndProcParsing(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	adapter.procNetDir = t.TempDir()
	spec := testSpec()
	if _, err := adapter.Export(context.Background(), spec, testCredential()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	aclPath := path.Join("target/iscsi", spec.TargetName, "tpgt_1/acls", spec.Initiator)
	writeMetricFile(t, fs.ConfigFS, path.Join(aclPath, "info"),
		"InitiatorName: "+spec.Initiator+"\n   Address 192.0.2.10 TCP  StatSN: 0x1\n")
	writeMetricFile(t, fs.ConfigFS, "target/iscsi/"+spec.TargetName+"/tpgt_1/acls/other/info",
		"InitiatorName: other\n   Address 192.0.2.11 TCP  StatSN: 0x1\n")
	procData := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0CBC 0A0200C0:9C40 01 00000000:00000000 00:00000000 00000000     0        0 1\n" +
		"   1: 0100007F:270F 0A0200C0:9C41 01 00000000:00000000 00:00000000 00000000     0        0 2\n" +
		"   2: 0100007F:0CBC 0A0200C0:9C42 06 00000000:00000000 00:00000000 00000000     0        0 3\n"
	if err := os.WriteFile(path.Join(adapter.procNetDir, "tcp"), []byte(procData), 0o600); err != nil {
		t.Fatal(err)
	}

	metrics, err := adapter.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if metrics.ConnectionError != "" {
		t.Fatalf("connection error: %s", metrics.ConnectionError)
	}
	if len(metrics.Connections) != 1 {
		t.Fatalf("connections = %#v", metrics.Connections)
	}
	connection := metrics.Connections[0]
	if connection.ClientAddress != "192.0.2.10" || connection.ClientPort != 40000 ||
		connection.LocalAddress != "127.0.0.1" || connection.LocalPort != 3260 ||
		connection.Target != spec.TargetName || connection.Initiator != spec.Initiator ||
		connection.Ambiguous {
		t.Fatalf("connection = %#v", connection)
	}

	clientAddress, port, err := parseProcEndpoint("0A0200C0:9C40")
	if err != nil || clientAddress != "192.0.2.10" || port != 40000 {
		t.Fatalf("endpoint = %q %d %v", clientAddress, port, err)
	}
	ipv6, err := parseProcAddress("00000000000000000000000000000000")
	if err != nil || ipv6 != "::" {
		t.Fatalf("IPv6 = %q %v", ipv6, err)
	}
	if _, err := parseProcAddress("bad"); err == nil {
		t.Fatal("invalid address accepted")
	}
	if _, _, err := parseProcEndpoint("bad"); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}

func TestLUNIndexAndResourceFilter(t *testing.T) {
	if index, err := parseLUNIndex("lun_12"); err != nil || index != 12 {
		t.Fatalf("lun index = %d, %v", index, err)
	}
	if _, err := parseLUNIndex("not-a-lun"); err == nil {
		t.Fatal("invalid LUN index accepted")
	}
	if mbytes(-1) != 0 || mbytes(2) != 2*1024*1024 {
		t.Fatal("mbytes conversion failed")
	}
}
