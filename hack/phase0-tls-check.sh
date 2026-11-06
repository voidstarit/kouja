#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "phase0-tls-check.sh must run as root" >&2
  exit 1
fi

VG=kouja-phase0-tls
LV=tlsprobe-lv
IMG=/var/tmp/kouja-phase0-tls.img
NQN=nqn.2026-11.io.kouja:tlsprobe
PORT=4422
HOSTNQN=$(cat /etc/nvme/hostnqn)
LOOP=""
TLS_LOGIN=0
HOST_CREATED=0

cleanup() {
  set +e
  nvme disconnect -n "$NQN" >/dev/null 2>&1
  rm -f "/sys/kernel/config/nvmet/ports/2/subsystems/$NQN" 2>/dev/null
  rm -f "/sys/kernel/config/nvmet/subsystems/$NQN/allowed_hosts/$HOSTNQN" 2>/dev/null
  rmdir "/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1" 2>/dev/null
  rmdir "/sys/kernel/config/nvmet/subsystems/$NQN" 2>/dev/null
  rmdir /sys/kernel/config/nvmet/ports/2 2>/dev/null
  if [ "$HOST_CREATED" -eq 1 ]; then
    rmdir "/sys/kernel/config/nvmet/hosts/$HOSTNQN" 2>/dev/null
  fi
  vgremove -f "$VG" >/dev/null 2>&1
  if [ -n "$LOOP" ]; then
    pvremove -f "$LOOP" >/dev/null 2>&1
    losetup -d "$LOOP" >/dev/null 2>&1
  fi
  rm -f "$IMG"
}
trap cleanup EXIT

mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
mountpoint -q /sys/kernel/debug || mount -t debugfs none /sys/kernel/debug

modprobe loop || true
modprobe nvmet || true
modprobe nvmet_tcp || true
modprobe nvme_tcp || true

if ! grep -q '^keyrings=.*\.nvme' /etc/tlshd.conf; then
  sed -i '/^\[authenticate\]/a keyrings=.nvme' /etc/tlshd.conf
fi
systemctl restart tlshd

truncate -s 2G "$IMG"
LOOP=$(losetup --find --show "$IMG")
pvcreate -f "$LOOP" >/dev/null
vgcreate "$VG" "$LOOP" >/dev/null
lvcreate -y -L 1G -n "$LV" "$VG" >/dev/null
DEV="/dev/$VG/$LV"

if [ ! -d "/sys/kernel/config/nvmet/hosts/$HOSTNQN" ]; then
  mkdir -p "/sys/kernel/config/nvmet/hosts/$HOSTNQN"
  HOST_CREATED=1
fi

mkdir -p "/sys/kernel/config/nvmet/subsystems/$NQN"
echo 0 >"/sys/kernel/config/nvmet/subsystems/$NQN/attr_allow_any_host"
mkdir -p "/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1"
echo -n "$DEV" >"/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1/device_path"
echo 1 >"/sys/kernel/config/nvmet/subsystems/$NQN/namespaces/1/enable"
ln -s "/sys/kernel/config/nvmet/hosts/$HOSTNQN" "/sys/kernel/config/nvmet/subsystems/$NQN/allowed_hosts/$HOSTNQN"

mkdir -p /sys/kernel/config/nvmet/ports/2
echo tcp >/sys/kernel/config/nvmet/ports/2/addr_trtype
echo tls1.3 >/sys/kernel/config/nvmet/ports/2/addr_tsas
echo required >/sys/kernel/config/nvmet/ports/2/addr_treq
echo ipv4 >/sys/kernel/config/nvmet/ports/2/addr_adrfam
echo 127.0.0.1 >/sys/kernel/config/nvmet/ports/2/addr_traddr
echo "$PORT" >/sys/kernel/config/nvmet/ports/2/addr_trsvcid
ln -s "/sys/kernel/config/nvmet/subsystems/$NQN" "/sys/kernel/config/nvmet/ports/2/subsystems/$NQN"

PSK=$(nvme gen-tls-key --subsysnqn="$NQN")
nvme check-tls-key --insert --hostnqn="$HOSTNQN" --subsysnqn="$NQN" --keydata="$PSK" --identity=1

if nvme connect -t tcp -a 127.0.0.1 -s "$PORT" -n "$NQN" --hostnqn="$HOSTNQN" --tls; then
  TLS_LOGIN=1
  for _ in $(seq 1 20); do
    if nvme list-subsys 2>/dev/null | grep -q "$NQN"; then
      break
    fi
    sleep 1
  done
  nvme list-subsys || true
  nvme list || true
  ls -l /dev/nvme* 2>/dev/null || true
  NS=$(ls /dev/nvme*n1 2>/dev/null | head -1 || true)
  if [ -n "$NS" ]; then
    if dd if=/dev/zero of="$NS" bs=1M count=8 conv=fsync status=none && dd if="$NS" of=/dev/null bs=1M count=8 status=none; then
      echo "io_check=ok device=$NS"
    else
      echo "io_check=failed device=$NS" >&2
    fi
  fi
  if [ -d /sys/kernel/debug/nvmet ]; then
    echo "nvmet_session_interface=debugfs"
    find /sys/kernel/debug/nvmet -maxdepth 3 2>/dev/null | head -30 || true
  elif [ -d "/sys/kernel/config/nvmet/subsystems/$NQN/controllers" ]; then
    echo "nvmet_session_interface=configfs_controllers"
  else
    echo "nvmet_session_interface=none"
  fi
else
  echo "nvme connect failed" >&2
  dmesg | tail -30 >&2 || true
fi

printf 'tls_login=%s subsystem=%s port=%s\n' "$TLS_LOGIN" "$NQN" "$PORT"
if [ "$TLS_LOGIN" -eq 1 ]; then
  exit 0
fi
exit 1
