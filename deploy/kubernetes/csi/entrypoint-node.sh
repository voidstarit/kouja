#!/bin/sh
set -eu

mkdir -p /run/iscsi /etc/iscsi
if [ ! -f /etc/iscsi/initiatorname.iscsi ]; then
  printf 'InitiatorName=%s\n' "${KOUJA_ISCSI_INITIATOR:-iqn.2026-11.io.kouja:kind-node}" >/etc/iscsi/initiatorname.iscsi
fi
# A host-mounted /run carries the host iscsid pid; use that daemon when
# present. Otherwise start a private daemon for environments such as kind.
if [ ! -s /run/iscsid.pid ]; then
  iscsid -f &
  sleep 1
fi
exec /usr/local/bin/kouja-csi-node "$@"
