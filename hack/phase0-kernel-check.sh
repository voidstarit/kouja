#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "phase0-kernel-check.sh must run as root" >&2
  exit 1
fi

VG=kouja-phase0
LV=phase0-lv
IMG=/var/tmp/kouja-phase0.img
LOOP=""
NQN=nqn.2026-11.io.kouja:phase0
IQN=iqn.2026-11.io.kouja:phase0
ISCSI_PORT=3261
NVME_PORT=4421
LIO_OK=0
NVME_OK=0
TLS_OK=0
TLS_TARGET_CONFIG=0
TLS_HOST_CONFIG=0
TLS_TARGET_AUTH_CONFIG=0
TREQ_OK=0
NVMET_CTRL_IFACE=0

kernel_config_enabled() {
  local symbol="$1"
  if [ -r "/boot/config-$(uname -r)" ]; then
    grep -Eq "^${symbol}=[ym]" "/boot/config-$(uname -r)"
    return
  fi
  if [ -r /proc/config.gz ]; then
    zgrep -Eq "^${symbol}=[ym]" /proc/config.gz
    return
  fi
  return 1
}

cleanup() {
  set +e
  targetcli /iscsi delete "$IQN" >/dev/null 2>&1
  targetcli /backstores/block delete phase0 >/dev/null 2>&1
  rm -f "/sys/kernel/config/nvmet/ports/1/subsystems/$NQN" 2>/dev/null
  rmdir "/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1" 2>/dev/null
  rmdir "/sys/kernel/config/nvmet/subsystems/$NQN" 2>/dev/null
  rmdir /sys/kernel/config/nvmet/ports/1 2>/dev/null
  vgremove -f "$VG" >/dev/null 2>&1
  pvremove -f "$LOOP" >/dev/null 2>&1
  if [ -n "$LOOP" ]; then losetup -d "$LOOP" >/dev/null 2>&1; fi
  rm -f "$IMG"
}
trap cleanup EXIT

mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config

modprobe loop || true
modprobe target_core_mod || true
modprobe iscsi_target_mod || true
modprobe nvmet || true
modprobe nvmet_tcp || true

truncate -s 2G "$IMG"
LOOP=$(losetup --find --show "$IMG")
pvcreate -f "$LOOP" >/dev/null
vgcreate "$VG" "$LOOP" >/dev/null
lvcreate -y -L 1G -n "$LV" "$VG" >/dev/null
DEV="/dev/$VG/$LV"

if command -v targetcli >/dev/null 2>&1; then
  targetcli /backstores/block create name=phase0 dev="$DEV" >/dev/null
  targetcli /iscsi create "$IQN" >/dev/null
  targetcli "/iscsi/$IQN/tpg1/luns" create /backstores/block/phase0 >/dev/null
  targetcli "/iscsi/$IQN/tpg1/portals" create 127.0.0.1 "$ISCSI_PORT" >/dev/null
  targetcli "/iscsi/$IQN/tpg1" set attribute authentication=0 >/dev/null
  echo 1 >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/enable"
  targetcli ls "/iscsi/$IQN" >/dev/null
  LIO_OK=1
else
  echo "targetcli is not installed; LIO check skipped" >&2
fi

mkdir -p "/sys/kernel/config/nvmet/subsystems/$NQN"
echo 1 >"/sys/kernel/config/nvmet/subsystems/$NQN/attr_allow_any_host"
mkdir -p "/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1"
echo -n "$DEV" >"/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1/device_path"
echo 1 >"/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1/enable"
mkdir -p /sys/kernel/config/nvmet/ports/1
echo tcp >/sys/kernel/config/nvmet/ports/1/addr_trtype
TLS_RC=0
echo tls1.3 2>/dev/null >/sys/kernel/config/nvmet/ports/1/addr_tsas || TLS_RC=$?
if [ "$TLS_RC" -eq 0 ]; then
  TLS_OK=1
fi
TREQ_RC=0
echo required 2>/dev/null >/sys/kernel/config/nvmet/ports/1/addr_treq || TREQ_RC=$?
if [ "$TREQ_RC" -eq 0 ]; then
  TREQ_OK=1
fi
echo ipv4 >/sys/kernel/config/nvmet/ports/1/addr_adrfam
echo 127.0.0.1 >/sys/kernel/config/nvmet/ports/1/addr_traddr
echo "$NVME_PORT" >/sys/kernel/config/nvmet/ports/1/addr_trsvcid
ln -s "/sys/kernel/config/nvmet/subsystems/$NQN" "/sys/kernel/config/nvmet/ports/1/subsystems/$NQN"
[ -e "/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1/enable" ]
NVME_OK=1

if kernel_config_enabled CONFIG_NVME_TARGET_TCP_TLS; then
  TLS_TARGET_CONFIG=1
fi
if kernel_config_enabled CONFIG_NVME_TCP_TLS; then
  TLS_HOST_CONFIG=1
fi
if kernel_config_enabled CONFIG_NVME_TARGET_AUTH; then
  TLS_TARGET_AUTH_CONFIG=1
fi
if [ -d "/sys/kernel/config/nvmet/subsystems/$NQN/controllers" ]; then
  NVMET_CTRL_IFACE=1
fi

printf 'lio=%s nvmet=%s tls_psk_port=%s tls_treq_required=%s tls_target_config=%s tls_host_config=%s tls_target_auth_config=%s nvmet_controllers_interface=%s iscsi_port=%s nvme_port=%s\n' "$LIO_OK" "$NVME_OK" "$TLS_OK" "$TREQ_OK" "$TLS_TARGET_CONFIG" "$TLS_HOST_CONFIG" "$TLS_TARGET_AUTH_CONFIG" "$NVMET_CTRL_IFACE" "$ISCSI_PORT" "$NVME_PORT"
printf 'subsystem=%s\n' "$NQN"

if [ "$LIO_OK" -eq 1 ] && [ "$NVME_OK" -eq 1 ]; then
  exit 0
fi
exit 1
