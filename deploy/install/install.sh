#!/usr/bin/env bash
set -euo pipefail

BIN_SRC="${1:-bin/kouja-agent}"
BIN_DST=/usr/local/bin/kouja-agent
CONFIG_DIR=/etc/kouja
DATA_DIR=/var/lib/kouja
ISCSI_ADDR="${KOUJA_ISCSI_ADDR:-0.0.0.0}"
ISCSI_PORT="${KOUJA_ISCSI_PORT:-3260}"
BACKUP_TARGET="${KOUJA_BACKUP_TARGET:-}"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must run as root" >&2
  exit 1
fi

install -D -m 0755 "$BIN_SRC" "$BIN_DST"
install -D -m 0644 deploy/systemd/kouja-agent.service /etc/systemd/system/kouja-agent.service
install -D -m 0644 deploy/systemd/kouja-agent.conf /etc/tmpfiles.d/kouja-agent.conf
install -d -m 0700 "$CONFIG_DIR" "$CONFIG_DIR/tls" "$DATA_DIR" "$DATA_DIR/backups"

if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
  cat >"$CONFIG_DIR/config.yaml" <<YAML
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 0.0.0.0:9443
  tls:
    cert_file: /etc/kouja/tls/server.crt
    key_file: /etc/kouja/tls/server.key
    client_ca_file: /etc/kouja/tls/client-ca.crt
    min_version: TLS1.3
storage:
  headroom_percent: 5
  headroom_min_bytes: 1073741824
  wipe_timeout_seconds: 1800
  vgs: []
  classes: {}
targets:
  iscsi:
    enabled: true
    iqn_prefix: iqn.2026-11.io.kouja
    portals:
      - address: ${ISCSI_ADDR}
        port: ${ISCSI_PORT}
  nvme_tcp:
    enabled: false
operations:
  max_attempts: 3
  total_timeout_seconds: 300
admission:
  max_concurrent_operations: 64
  per_caller_operations: 10
  capacity_requests_per_second: 10
state:
  backup_dir: /var/lib/kouja/backups
  backup_target: ${BACKUP_TARGET}
YAML
  chmod 0600 "$CONFIG_DIR/config.yaml"
fi

if [ ! -s "$CONFIG_DIR/tls/server.crt" ] || [ ! -s "$CONFIG_DIR/tls/server.key" ] || [ ! -s "$CONFIG_DIR/tls/client-ca.crt" ]; then
  echo "TLS files are required under $CONFIG_DIR/tls before starting kouja" >&2
  exit 1
fi

if systemctl list-unit-files target.service >/dev/null 2>&1; then
  systemctl disable --now target.service || true
fi
rm -f /etc/target/saveconfig.json

systemd-tmpfiles --create /etc/tmpfiles.d/kouja-agent.conf
systemctl daemon-reload
systemctl enable --now kouja-agent.service

cat <<'EOF'
kouja is installed.
Firewall ports: 9443/tcp control, 3260/tcp iSCSI, 4420/tcp NVMe-oF.
Uninstall: systemctl disable --now kouja-agent; rm -f /etc/systemd/system/kouja-agent.service /etc/tmpfiles.d/kouja-agent.conf /usr/local/bin/kouja-agent.
Data volumes are not deleted by uninstall.
EOF
