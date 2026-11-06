#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "phase0-failure-check.sh must run as root" >&2
  exit 1
fi

VG=kouja-phase0-failure
LV=failure-lv
IMG=/var/tmp/kouja-phase0-failure.img
IQN=iqn.2026-11.io.kouja:failure
IQN2=iqn.2026-11.io.kouja:failure-port
IQN3=iqn.2026-11.io.kouja:failure-partial
INITIATOR=iqn.2026-11.io.kouja:initiator
PORT=3264
CHAP_USER=kouja-phase0-user
CHAP_PASS=kouja-phase0-pass
LOOP=""
LOGIN=0
ORIG_INITIATOR=""
DELETE_ACTIVE_RC=0
LVREMOVE_ACTIVE_RC=0
DUPLICATE_PORTAL_MKDIR_RC=0
DUPLICATE_PORTAL_ENABLE_RC=0
PARTIAL_CLEANUP_RC=0

cleanup() {
  set +e
  if [ "$LOGIN" -eq 1 ]; then
    iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --logout >/dev/null 2>&1
  fi
  iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" -o delete >/dev/null 2>&1
  targetcli /iscsi delete "$IQN" >/dev/null 2>&1
  targetcli /iscsi delete "$IQN2" >/dev/null 2>&1
  targetcli /iscsi delete "$IQN3" >/dev/null 2>&1
  targetcli /backstores/block delete phase0 >/dev/null 2>&1
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
targetcli /iscsi delete "$IQN2" >/dev/null 2>&1 || true
targetcli /iscsi delete "$IQN3" >/dev/null 2>&1 || true
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

mkdir -p /sys/kernel/config/target/core/iblock_0/phase0
printf 'udev_path=%s\n' "$DEV" >/sys/kernel/config/target/core/iblock_0/phase0/control
printf '1' >/sys/kernel/config/target/core/iblock_0/phase0/enable

mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1"
printf '0' >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/attrib/authentication"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/np/127.0.0.1:$PORT"
printf '1' >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/enable"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0"
ln -s /sys/kernel/config/target/core/iblock_0/phase0 "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0/phase0"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR"
mkdir -p "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0"
ln -s "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/lun/lun_0" "/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0/0"
printf '0' >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/lun_0/write_protect"
printf '%s' "$CHAP_USER" >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/auth/userid"
printf '%s' "$CHAP_PASS" >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/acls/$INITIATOR/auth/password"
printf '1' >"/sys/kernel/config/target/iscsi/$IQN/tpgt_1/attrib/authentication"

iscsiadm -m node -o new -T "$IQN" -p "127.0.0.1:$PORT" >/dev/null 2>&1 || true
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.authmethod -v CHAP
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.username -v "$CHAP_USER"
iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --op update -n node.session.auth.password -v "$CHAP_PASS"
if iscsiadm -m node -T "$IQN" -p "127.0.0.1:$PORT" --login >/tmp/kouja-failure-login.out 2>&1; then
  LOGIN=1
fi
sleep 1
SESSIONS=$(cat "/sys/kernel/config/target/iscsi/$IQN/fabric_statistics/iscsi_instance/sessions" 2>/dev/null || echo unknown)

mkdir -p "/sys/kernel/config/target/iscsi/$IQN2/tpgt_1"
set +e
mkdir "/sys/kernel/config/target/iscsi/$IQN2/tpgt_1/np/127.0.0.1:$PORT" >/tmp/kouja-failure-port.out 2>&1
DUPLICATE_PORTAL_MKDIR_RC=$?
printf '1' 2>/dev/null >"/sys/kernel/config/target/iscsi/$IQN2/tpgt_1/enable"
DUPLICATE_PORTAL_ENABLE_RC=$?
set -e
targetcli /iscsi delete "$IQN2" >/dev/null 2>&1 || true

set +e
lvremove --yes "$VG/$LV" >/tmp/kouja-failure-lvremove.out 2>&1
LVREMOVE_ACTIVE_RC=$?
set -e
LV_AFTER_REMOVE=no
if [ -e "/dev/$VG/$LV" ]; then LV_AFTER_REMOVE=yes; fi

set +e
targetcli /iscsi delete "$IQN" >/tmp/kouja-failure-delete.out 2>&1
DELETE_ACTIVE_RC=$?
set -e
TARGET_AFTER_DELETE=no
if [ -d "/sys/kernel/config/target/iscsi/$IQN" ]; then TARGET_AFTER_DELETE=yes; fi

mkdir -p "/sys/kernel/config/target/iscsi/$IQN3/tpgt_1"
targetcli /iscsi delete "$IQN3" >/tmp/kouja-failure-partial.out 2>&1
if [ ! -d "/sys/kernel/config/target/iscsi/$IQN3" ]; then PARTIAL_CLEANUP_RC=0; else PARTIAL_CLEANUP_RC=1; fi

printf 'session_count=%s\n' "$SESSIONS"
printf 'delete_with_active_session_rc=%s target_after_delete=%s\n' "$DELETE_ACTIVE_RC" "$TARGET_AFTER_DELETE"
printf 'lvremove_with_active_session_rc=%s lv_after_remove=%s\n' "$LVREMOVE_ACTIVE_RC" "$LV_AFTER_REMOVE"
printf 'duplicate_portal_mkdir_rc=%s enable_rc=%s\n' "$DUPLICATE_PORTAL_MKDIR_RC" "$DUPLICATE_PORTAL_ENABLE_RC"
printf 'partial_target_cleanup_rc=%s\n' "$PARTIAL_CLEANUP_RC"
printf '%s\n' "--- delete output ---"
sed -n '1,8p' /tmp/kouja-failure-delete.out
printf '%s\n' "--- lvremove output ---"
sed -n '1,8p' /tmp/kouja-failure-lvremove.out
printf '%s\n' "--- duplicate portal output ---"
sed -n '1,8p' /tmp/kouja-failure-port.out

if [ "$SESSIONS" != "unknown" ] && [ "$SESSIONS" -ge 1 ] &&
   [ "$PARTIAL_CLEANUP_RC" -eq 0 ]; then
  exit 0
fi
exit 1
