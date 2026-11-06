#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "phase0-lio-check.sh must run as root" >&2
  exit 1
fi

VG=kouja-phase0-lio
LV=lioprobe-lv
IMG=/var/tmp/kouja-phase0-lio.img
IQN=iqn.2026-11.io.kouja:lioprobe
INITIATOR=iqn.2026-11.io.kouja:initiator
PORT=3262
CHAP_USER=kouja-phase0-user
CHAP_PASS=kouja-phase0-pass
LOOP=""
LOGIN=0
ORIG_INITIATOR=""

cleanup() {
  set +e
  if [ "$LOGIN" -eq 1 ]; then
    iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --logout >/dev/null 2>&1
  fi
  iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" -o delete >/dev/null 2>&1
  targetcli /iscsi delete "$IQN" >/dev/null 2>&1
  targetcli /backstores/block delete phase0 >/dev/null 2>&1
  rmdir "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/np/127.0.0.1:$PORT" 2>/dev/null
  rm -f "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0/phase0" 2>/dev/null
  rmdir "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0" 2>/dev/null
  rm -f "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0/0" 2>/dev/null
  rmdir "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0" 2>/dev/null
  rmdir "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR" 2>/dev/null
  rmdir "/sys/kernel/config/target/iscsi/$IQN/tpgt_1" 2>/dev/null
  rmdir "/sys/kernel/config/target/iscsi/$IQN" 2>/dev/null
  if [ -e /sys/kernel/config/target/core/iblock_0/phase0/enable ]; then
    printf '0' >/sys/kernel/config/target/core/iblock_0/phase0/enable 2>/dev/null
  fi
  rmdir /sys/kernel/config/target/core/iblock_0/phase0 2>/dev/null
  vgremove -f "$VG" >/dev/null 2>&1
  if [ -n "$LOOP" ]; then
    pvremove -f "$LOOP" >/dev/null 2>&1
    losetup -d "$LOOP" >/dev/null 2>&1
  fi
  rm -f "$IMG"
  if [ -n "$ORIG_INITIATOR" ]; then
    printf '%s\n' "$ORIG_INITIATOR" >/etc/iscsi/initiatorname.iscsi
    systemctl restart iscsid >/dev/null 2>&1
  fi
}
trap cleanup EXIT

mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
modprobe loop || true
modprobe target_core_mod || true
modprobe target_core_iblock || true
modprobe iscsi_target_mod || true

targetcli /iscsi delete "$IQN" >/dev/null 2>&1 || true
targetcli /backstores/block delete phase0 >/dev/null 2>&1 || true

ORIG_INITIATOR=$(cat /etc/iscsi/initiatorname.iscsi 2>/dev/null || true)
printf 'InitiatorName=%s\n' "$INITIATOR" >/etc/iscsi/initiatorname.iscsi
systemctl start iscsid
systemctl restart iscsid

truncate -s 2G "$IMG"
LOOP=$(losetup --find --show "$IMG")
pvcreate -f "$LOOP" >/dev/null
vgcreate "$VG" "$LOOP" >/dev/null
lvcreate -y -L 1G -n "$LV" "$VG" >/dev/null
DEV="/dev/$VG/$LV"

# iblock parameters are written through control; the udev_path attribute is
# informational and does not configure the backend.
mkdir -p /sys/kernel/config/target/core/iblock_0/phase0
printf 'udev_path=%s\n' "$DEV" >/sys/kernel/config/target/core/iblock_0/phase0/control
echo 1 >/sys/kernel/config/target/core/iblock_0/phase0/enable

mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1"
echo 0 >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/attrib/authentication"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/np/127.0.0.1:$PORT"
echo 1 >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/enable"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0"
ln -s /sys/kernel/config/target/core/iblock_0/phase0 "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0/phase0"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0"
ln -s "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0" "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0/0"
printf '0' >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0/write_protect"
# The ACL auth attributes preserve bytes verbatim. A trailing newline becomes
# part of the CHAP name and breaks login with "CHAP_N values do not match".
printf '%s' "$CHAP_USER" >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/auth/userid"
printf '%s' "$CHAP_PASS" >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/auth/password"
echo 1 >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/attrib/authentication"

iscsiadm -m node -o new -T "$IQN" -p "127.0.0.1:$PORT" >/dev/null 2>&1 || true
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.authmethod -v CHAP
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.username -v "$CHAP_USER"
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.password -v "$CHAP_PASS"

if iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --login; then
  LOGIN=1
fi

sleep 1
LIO_SESSIONS=$(cat "/sys/kernel/config/target/iscsi/$IQN/fabric_statistics/iscsi_instance/sessions" 2>/dev/null || echo unknown)
targetcli sessions 2>&1 || true
iscsiadm -m session 2>&1 || true
lsblk -o NAME,SIZE,TYPE,TRAN 2>&1 || true

printf 'iscsi_login=%s lio_sessions=%s portal=127.0.0.1:%s target=%s\n' "$LOGIN" "$LIO_SESSIONS" "$PORT" "$IQN"
if [ "$LOGIN" -eq 1 ] && [ "$LIO_SESSIONS" != "unknown" ] && [ "$LIO_SESSIONS" -ge 1 ]; then
  exit 0
fi
exit 1
