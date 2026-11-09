#!/usr/bin/env bash
# Interactive kouja TLS + configuration initializer.
#
# This helper is intended for a fresh single-host installation. It can:
#   * generate a local test CA, server certificate, and client certificates;
#   * collect the common agent settings with prompts and sensible defaults;
#   * validate the generated YAML with `kouja-agent --check-config`;
#   * install the configuration and TLS files with restrictive permissions.
#
# For production, prefer certificates from your existing PKI. The local CA
# generated here is a convenience for self-contained hosts and testing.
set -euo pipefail
umask 077

GO="${GO:-go}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_PATH="${KOUJA_CONFIG_PATH:-/etc/kouja/config.yaml}"
TLS_DIR="${KOUJA_TLS_DIR:-$(dirname "$CONFIG_PATH")/tls}"
NONINTERACTIVE="${KOUJA_INIT_NONINTERACTIVE:-0}"

if [[ -t 1 ]]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
else
  BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; RESET=""
fi

say() { printf '%s\n' "$*"; }
warn() { printf '%sWARN:%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() { printf '%sERROR:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
step() { printf '\n%s==> %s%s\n' "$BOLD" "$*" "$RESET"; }

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

ask() {
  local prompt="$1" default="${2:-}" reply
  if [[ "$NONINTERACTIVE" == "1" ]]; then
    printf '%s [%s]\n' "$prompt" "$default" >&2
    printf '%s' "$default"
    return 0
  fi
  if [[ -n "$default" ]]; then
    printf '%s [%s]: ' "$prompt" "$default" >&2
  else
    printf '%s: ' "$prompt" >&2
  fi
  read -r reply || reply=""
  printf '%s' "${reply:-$default}"
}

ask_yes_no() {
  local prompt="$1" default="${2:-n}" reply
  if [[ "$NONINTERACTIVE" == "1" ]]; then
    printf '%s [%s]\n' "$prompt" "$default" >&2
    [[ "$default" == "y" || "$default" == "Y" ]]
    return $?
  fi
  printf '%s [%s]: ' "$prompt" "$default" >&2
  read -r reply || reply=""
  reply="${reply:-$default}"
  case "$reply" in
    y|Y|yes|YES) return 0 ;;
    n|N|no|NO) return 1 ;;
    *) return 1 ;;
  esac
}

yaml_quote() {
  local value="$1"
  value="${value//\'/\'\'}"
  printf "'%s'" "$value"
}

is_loopback_address() {
  [[ "$1" =~ ^127\.0\.0\.1:[0-9]+$ || "$1" =~ ^\[::1\]:[0-9]+$ ]]
}

is_ipv4() {
  [[ "$1" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]
}

server_san_entries() {
  local input="$1" item output=""
  IFS=',' read -r -a parts <<< "$input"
  for item in "${parts[@]}"; do
    item="$(trim "$item")"
    [[ -z "$item" ]] && continue
    if is_ipv4 "$item" || [[ "$item" == *:* ]]; then
      output+="IP:${item},"
    else
      output+="DNS:${item},"
    fi
  done
  printf '%s' "${output%,}"
}

die_if_missing_file() {
  local path="$1" label="$2"
  [[ -n "$path" && -r "$path" ]] || die "$label is required and must be readable: $path"
}

gen_ca() {
  say "Generating local CA..."
  openssl genrsa -out "$CA_KEY" 4096 >/dev/null 2>&1
  openssl req -x509 -new -nodes -key "$CA_KEY" -sha256 -days 3650 \
    -subj "/CN=kouja-local-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "$CA_CERT" >/dev/null 2>&1
  chmod 0600 "$CA_KEY"
  chmod 0644 "$CA_CERT"
}

gen_server() {
  local san
  san="$(server_san_entries "$SERVER_SANS")"
  [[ -n "$san" ]] || die "at least one server DNS name or IP SAN is required"
  say "Generating server certificate for SANs: $san"
  openssl genrsa -out "$SERVER_KEY" 3072 >/dev/null 2>&1
  openssl req -new -key "$SERVER_KEY" -subj "/CN=kouja-agent" \
    -out "$WORKDIR/server.csr" >/dev/null 2>&1
  cat >"$WORKDIR/server.ext" <<EOF
[server_ext]
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=${san}
EOF
  openssl x509 -req -in "$WORKDIR/server.csr" -CA "$CA_CERT" -CAkey "$CA_KEY" \
    -CAcreateserial -out "$SERVER_CERT" -days 825 -sha256 \
    -extfile "$WORKDIR/server.ext" -extensions server_ext >/dev/null 2>&1
  chmod 0600 "$SERVER_KEY"
  chmod 0644 "$SERVER_CERT"
}

gen_client() {
  local role="$1" name="$2"
  local cert="$TLS_DIR/clients/${role}-${name}.crt"
  local key="$TLS_DIR/clients/${role}-${name}.key"
  local csr="$WORKDIR/${role}-${name}.csr"
  local ext="$WORKDIR/${role}-${name}.ext"
  if [[ -e "$cert" || -e "$key" ]]; then
    if ! ask_yes_no "Overwrite existing ${role} client certificate for ${name}?" n; then
      say "Keeping existing $cert"
      return 0
    fi
  fi
  say "Generating ${role} client certificate: spiffe://kouja/${role}/${name}"
  openssl genrsa -out "$key" 3072 >/dev/null 2>&1
  openssl req -new -key "$key" -subj "/CN=kouja-${role}-${name}" -out "$csr" >/dev/null 2>&1
  cat >"$ext" <<EOF
[client_ext]
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
subjectAltName=URI:spiffe://kouja/${role}/${name}
EOF
  openssl x509 -req -in "$csr" -CA "$CA_CERT" -CAkey "$CA_KEY" \
    -CAcreateserial -out "$cert" -days 825 -sha256 \
    -extfile "$ext" -extensions client_ext >/dev/null 2>&1
  chmod 0600 "$key"
  chmod 0644 "$cert"
  if [[ "$role" == "admin" ]]; then
    ADMIN_CERT="$cert"
    ADMIN_KEY="$key"
  fi
  CLIENT_CERTS+=("$cert")
}

say "${BOLD}kouja local setup${RESET}"
say "This helper writes a configuration file and, optionally, a local test PKI."
warn "For production, use certificates from your operator PKI. The generated CA is for convenience/testing."
if [[ "$EUID" -ne 0 && "$CONFIG_PATH" == /etc/* ]]; then
  warn "You are not root; writing $CONFIG_PATH may fail. Re-run with sudo or choose a writable path."
fi

step "1/7 Configuration paths"
CONFIG_PATH="$(ask "Configuration file" "$CONFIG_PATH")"
if [[ -e "$CONFIG_PATH" ]]; then
  if ! ask_yes_no "Configuration exists. Overwrite it (a timestamped .bak is kept)?" n; then
    die "aborted; existing configuration left unchanged"
  fi
fi
TLS_DIR="$(ask "TLS directory" "$TLS_DIR")"
HOST_ID_FILE="$(ask "Host ID file" "/var/lib/kouja/host-id")"
DATA_DIR="$(ask "Data directory" "/var/lib/kouja")"
SECRET_KEY_FILE="$(ask "Credential secret key file" "/var/lib/kouja/secret.key")"
EMERGENCY_LOG="$(ask "Emergency log file" "/var/lib/kouja/emergency.log")"

step "2/7 Control and admin listeners"
LISTEN_ADDRESS="$(ask "mTLS listener address" "0.0.0.0:9443")"
ADMIN_ENABLED=0
ADMIN_ADDRESS="127.0.0.1:9444"
if ask_yes_no "Enable the local admin UI? (loopback only, no authentication)" n; then
  ADMIN_ENABLED=1
  ADMIN_ADDRESS="$(ask "Admin UI address" "$ADMIN_ADDRESS")"
  while ! is_loopback_address "$ADMIN_ADDRESS"; do
    warn "Admin address must be exactly 127.0.0.1:<port> or [::1]:<port>"
    ADMIN_ADDRESS="$(ask "Admin UI address" "$ADMIN_ADDRESS")"
  done
fi

step "3/7 Storage"
VGS_RAW="$(ask "Volume groups to seed on first start (comma-separated, blank for none)" "")"
CLASSES_RAW="$(ask "Storage-class mappings (comma-separated class=vg, blank for none)" "")"

step "4/7 iSCSI target"
ISCSI_ENABLED=0
ISCSI_PORTAL="0.0.0.0:3260"
IQN_PREFIX="iqn.2026-11.io.kouja"
if ask_yes_no "Enable the iSCSI target?" y; then
  ISCSI_ENABLED=1
  IQN_PREFIX="$(ask "iSCSI IQN prefix" "$IQN_PREFIX")"
  ISCSI_PORTAL="$(ask "iSCSI portal address:port" "$ISCSI_PORTAL")"
fi
ISCSI_ADDR="${ISCSI_PORTAL%:*}"
ISCSI_PORT="${ISCSI_PORTAL##*:}"

step "5/7 NVMe/TCP target"
NVME_ENABLED=0
NVME_PORTAL="0.0.0.0:4420"
NQN_PREFIX="nqn.2026-11.io.kouja"
NVME_KEYRING=".nvme"
NVME_TLSHD="tlshd"
if ask_yes_no "Enable the NVMe/TCP target? (requires nvmet, nvmet_tcp, and tlshd)" n; then
  NVME_ENABLED=1
  NQN_PREFIX="$(ask "NVMe NQN prefix" "$NQN_PREFIX")"
  NVME_PORTAL="$(ask "NVMe/TCP portal address:port" "$NVME_PORTAL")"
  NVME_KEYRING="$(ask "NVMe/TCP TLS keyring" "$NVME_KEYRING")"
  NVME_TLSHD="$(ask "NVMe/TCP tlshd service name" "$NVME_TLSHD")"
fi
NVME_ADDR="${NVME_PORTAL%:*}"
NVME_PORT="${NVME_PORTAL##*:}"

step "6/7 Backup"
BACKUP_DIR="$(ask "Local backup directory" "/var/lib/kouja/backups")"
BACKUP_TARGET="$(ask "Off-host backup target (blank to disable backups)" "")"

step "7/7 TLS"
SERVER_CERT="$TLS_DIR/server.crt"
SERVER_KEY="$TLS_DIR/server.key"
CLIENT_CA="$TLS_DIR/client-ca.crt"
CA_CERT="$TLS_DIR/ca.crt"
CA_KEY="$TLS_DIR/ca.key"
CLIENT_CERTS=()

if ask_yes_no "Generate a local test CA and certificates?" y; then
  command -v openssl >/dev/null 2>&1 || die "openssl is required to generate certificates"
  WORKDIR="$(mktemp -d)"
  trap 'rm -rf "$WORKDIR"' EXIT
  mkdir -p "$TLS_DIR/clients"
  chmod 0700 "$TLS_DIR" "$TLS_DIR/clients"

  if [[ -s "$CA_CERT" && -s "$CA_KEY" && -s "$SERVER_CERT" && -s "$SERVER_KEY" ]]; then
    if ask_yes_no "Reuse the existing local CA and server certificate?" y; then
      say "Reusing existing CA and server certificate."
    else
      if ! ask_yes_no "Back up and replace the existing TLS files?" n; then
        die "aborted; existing TLS files left unchanged"
      fi
      BACKUP_TLS="$TLS_DIR.bak.$(date +%Y%m%d%H%M%S)"
      cp -a "$TLS_DIR" "$BACKUP_TLS"
      say "TLS backup: $BACKUP_TLS"
      gen_ca
      SERVER_SANS="$(ask "Server DNS names/IPs (comma-separated)" "$(hostname -f 2>/dev/null || hostname),localhost,127.0.0.1")"
      gen_server
    fi
  elif [[ -e "$CA_CERT" || -e "$CA_KEY" || -e "$SERVER_CERT" || -e "$SERVER_KEY" ]]; then
    if ! ask_yes_no "Existing TLS files are incomplete. Back up and replace them?" n; then
      die "aborted; existing TLS files left unchanged"
    fi
    BACKUP_TLS="$TLS_DIR.bak.$(date +%Y%m%d%H%M%S)"
    cp -a "$TLS_DIR" "$BACKUP_TLS"
    say "TLS backup: $BACKUP_TLS"
    gen_ca
    DEFAULT_SANS="$(hostname -f 2>/dev/null || hostname),localhost,127.0.0.1"
    PRIMARY_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    if [[ -n "$PRIMARY_IP" ]]; then
      DEFAULT_SANS="${DEFAULT_SANS},${PRIMARY_IP}"
    fi
    SERVER_SANS="$(ask "Server DNS names/IPs (comma-separated)" "$DEFAULT_SANS")"
    gen_server
  else
    gen_ca
    DEFAULT_SANS="$(hostname -f 2>/dev/null || hostname),localhost,127.0.0.1"
    PRIMARY_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    if [[ -n "$PRIMARY_IP" ]]; then
      DEFAULT_SANS="${DEFAULT_SANS},${PRIMARY_IP}"
    fi
    SERVER_SANS="$(ask "Server DNS names/IPs (comma-separated)" "$DEFAULT_SANS")"
    gen_server
  fi
  cp -f "$CA_CERT" "$CLIENT_CA"
  chmod 0644 "$CLIENT_CA"

  if ask_yes_no "Generate an admin client certificate for the mTLS API?" y; then
    ADMIN_NAME="$(ask "Admin client name" "admin")"
    gen_client admin "$ADMIN_NAME"
  fi
  if ask_yes_no "Generate a caller client certificate for volume operations?" y; then
    CALLER_NAME="$(ask "Caller client name" "caller")"
    gen_client caller "$CALLER_NAME"
  fi
  if ask_yes_no "Generate a controller client certificate for the CSI controller?" n; then
    CONTROLLER_NAME="$(ask "Controller name" "controller")"
    gen_client controller "$CONTROLLER_NAME"
  fi
  if ask_yes_no "Generate a node client certificate for the CSI node?" n; then
    NODE_NAME="$(ask "Node name" "node")"
    gen_client node "$NODE_NAME"
  fi
else
  say "Using existing certificate files."
  SERVER_CERT="$(ask "Server certificate" "$SERVER_CERT")"
  SERVER_KEY="$(ask "Server private key" "$SERVER_KEY")"
  CLIENT_CA="$(ask "Client CA certificate" "$CLIENT_CA")"
  die_if_missing_file "$SERVER_CERT" "Server certificate"
  die_if_missing_file "$SERVER_KEY" "Server private key"
  die_if_missing_file "$CLIENT_CA" "Client CA certificate"
fi

if [[ ${#CLIENT_CERTS[@]} -gt 0 ]]; then
  openssl verify -CAfile "$CA_CERT" "${CLIENT_CERTS[@]}" >/dev/null
fi
if [[ -n "${SERVER_SANS:-}" && -r "$CA_CERT" && -r "$SERVER_CERT" ]]; then
  openssl verify -CAfile "$CA_CERT" "$SERVER_CERT" >/dev/null
fi

step "Writing and validating configuration"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
trap 'rm -rf "$WORKDIR"' EXIT
TMP_CONFIG="$WORKDIR/config.yaml"

q() { yaml_quote "$1"; }

{
  echo "# Generated by kouja-init.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "host_id_file: $(q "$HOST_ID_FILE")"
  echo "data_dir: $(q "$DATA_DIR")"
  echo "secret_key_file: $(q "$SECRET_KEY_FILE")"
  echo "emergency_log: $(q "$EMERGENCY_LOG")"
  echo "listen:"
  echo "  address: $(q "$LISTEN_ADDRESS")"
  echo "  tls:"
  echo "    cert_file: $(q "$SERVER_CERT")"
  echo "    key_file: $(q "$SERVER_KEY")"
  echo "    client_ca_file: $(q "$CLIENT_CA")"
  echo "    min_version: TLS1.3"
  echo "admin_ui:"
  if [[ "$ADMIN_ENABLED" == "1" ]]; then
    echo "  enabled: true"
    echo "  address: $(q "$ADMIN_ADDRESS")"
  else
    echo "  enabled: false"
    echo "  address: 127.0.0.1:9444"
  fi
  echo "certificate_authority:"
  if [[ -f "$CA_KEY" && -n "${ADMIN_CERT:-}" ]]; then
    echo "  enabled: true"
  else
    echo "  enabled: false"
  fi
  echo "  key_file: $(q "$CA_KEY")"
  echo "  certificate_dir: $(q "$TLS_DIR/clients")"
  echo "  revocation_log_file: /var/lib/kouja/revoked-serials.log"
  echo "  default_ttl_days: 30"
  echo "  max_ttl_days: 365"
  echo "  max_active_certificates: 1000"
  echo "storage:"
  echo "  headroom_percent: 5"
  echo "  headroom_min_bytes: 1073741824"
  echo "  wipe_timeout_seconds: 1800"
  if [[ -n "$(trim "$VGS_RAW")" ]]; then
    echo "  vgs:"
    IFS=',' read -r -a vg_items <<< "$VGS_RAW"
    for item in "${vg_items[@]}"; do
      item="$(trim "$item")"
      [[ -z "$item" ]] && continue
      echo "    - $(q "$item")"
    done
  else
    echo "  vgs: []"
  fi
  if [[ -n "$(trim "$CLASSES_RAW")" ]]; then
    echo "  classes:"
    IFS=',' read -r -a class_items <<< "$CLASSES_RAW"
    for item in "${class_items[@]}"; do
      item="$(trim "$item")"
      [[ -z "$item" ]] && continue
      [[ "$item" == *=* ]] || die "class mapping must be class=vg: $item"
      class="${item%%=*}"
      vg="${item#*=}"
      echo "    $(q "$(trim "$class")"): $(q "$(trim "$vg")")"
    done
  else
    echo "  classes: {}"
  fi
  echo "targets:"
  echo "  iscsi:"
  if [[ "$ISCSI_ENABLED" == "1" ]]; then
    echo "    enabled: true"
  else
    echo "    enabled: false"
  fi
  echo "    iqn_prefix: $(q "$IQN_PREFIX")"
  echo "    portals:"
  echo "      - address: $(q "$ISCSI_ADDR")"
  echo "        port: ${ISCSI_PORT}"
  echo "  nvme_tcp:"
  if [[ "$NVME_ENABLED" == "1" ]]; then
    echo "    enabled: true"
  else
    echo "    enabled: false"
  fi
  echo "    nqn_prefix: $(q "$NQN_PREFIX")"
  echo "    portals:"
  echo "      - address: $(q "$NVME_ADDR")"
  echo "        port: ${NVME_PORT}"
  echo "    tls:"
  echo "      enabled: true"
  echo "      keyring: $(q "$NVME_KEYRING")"
  echo "      tlshd_service: $(q "$NVME_TLSHD")"
  echo "operations:"
  echo "  max_attempts: 3"
  echo "  total_timeout_seconds: 300"
  echo "  step_timeout_seconds:"
  echo "    create: 120"
  echo "    export: 60"
  echo "    delete: 120"
  echo "    resize: 120"
  echo "admission:"
  echo "  max_concurrent_operations: 64"
  echo "  per_caller_operations: 10"
  echo "  capacity_requests_per_second: 10"
  echo "observability:"
  echo "  otel:"
  echo "    enabled: false"
  echo "    endpoint: \"\""
  echo "    insecure: false"
  echo "    interval_seconds: 30"
  echo "    service_name: kouja-agent"
  echo "    headers: {}"
  echo "state:"
  echo "  backup_dir: $(q "$BACKUP_DIR")"
  echo "  backup_target: $(q "$BACKUP_TARGET")"
  echo "  backup_interval_seconds: 900"
  echo "  backup_retention_short: 96"
  echo "  backup_retention_daily: 7"
  echo "  backup_retention_weekly: 4"
  echo "  integrity_check_interval_seconds: 86400"
  echo "  reconcile_interval_seconds: 60"
  echo "  audit_retention_days: 90"
  echo "  s3_backup:"
  echo "    enabled: false"
  echo "    bucket: \"\""
  echo "    prefix: kouja/"
  echo "    region: \"\""
  echo "    endpoint: \"\""
  echo "    force_path_style: false"
  echo "    server_side_encryption: AES256"
  echo "    kms_key_id: \"\""
  echo "    per_backup_folder: true"
  echo "    retention_days: 7"
  echo "    encryption_key_file: \"\""
} >"$TMP_CONFIG"

AGENT_BIN="${KOUJA_AGENT_BIN:-$SCRIPT_DIR/kouja-agent}"
if [[ ! -x "$AGENT_BIN" && -x "$SCRIPT_DIR/../bin/kouja-agent" ]]; then
  AGENT_BIN="$SCRIPT_DIR/../bin/kouja-agent"
fi
if [[ -x "$AGENT_BIN" ]]; then
  "$AGENT_BIN" --check-config --config "$TMP_CONFIG"
elif command -v "$GO" >/dev/null 2>&1 && [[ -f "$SCRIPT_DIR/../go.mod" ]]; then
  (cd "$SCRIPT_DIR/.." && "$GO" run ./cmd/kouja-agent --check-config --config "$TMP_CONFIG")
else
  warn "kouja-agent binary not found; skipped YAML validation. Build with 'make all' or set KOUJA_AGENT_BIN."
fi

if [[ -e "$CONFIG_PATH" ]]; then
  BACKUP_CONFIG="${CONFIG_PATH}.bak.$(date +%Y%m%d%H%M%S)"
  cp -a "$CONFIG_PATH" "$BACKUP_CONFIG"
  say "Configuration backup: $BACKUP_CONFIG"
fi
install -D -m 0600 "$TMP_CONFIG" "$CONFIG_PATH"

say ""
say "${GREEN}Setup complete.${RESET}"
say "  Configuration: $CONFIG_PATH"
say "  Server cert:   $SERVER_CERT"
say "  Server key:    $SERVER_KEY"
say "  Client CA:     $CLIENT_CA"
if [[ ${#CLIENT_CERTS[@]} -gt 0 ]]; then
  say "  Client certs:"
  for cert in "${CLIENT_CERTS[@]}"; do
    say "    $cert"
  done
fi
if [[ "$ADMIN_ENABLED" == "1" ]]; then
  say "  Admin UI:      http://$ADMIN_ADDRESS"
  warn "The admin UI has no authentication; keep it loopback-only."
fi
HOST_FOR_CLIENT="${LISTEN_ADDRESS%:*}"
if [[ "$HOST_FOR_CLIENT" == "0.0.0.0" || "$HOST_FOR_CLIENT" == "::" ]]; then
  HOST_FOR_CLIENT="127.0.0.1"
fi
say ""
say "Next steps:"
say "  1. Review $CONFIG_PATH"
say "  2. Restart the agent: systemctl restart kouja-agent"
if [[ -n "${ADMIN_CERT:-}" ]]; then
  say "  3. Test mTLS with the generated admin certificate:"
  say "     curl --cacert $(q "$CLIENT_CA") --cert $(q "$ADMIN_CERT") \\"
  say "          --key $(q "$ADMIN_KEY") https://${HOST_FOR_CLIENT}:9443/v1/admin/vgs"
else
  say "  3. Test mTLS with your existing client certificate."
fi
