#!/usr/bin/env bash
set -u

fail=0

check_required() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    printf '%-30s %s\n' "$name" "ok"
  else
    printf '%-30s %s\n' "$name" "missing"
    fail=1
  fi
}

check_optional() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    printf '%-30s %s\n' "$name" "ok"
  else
    printf '%-30s %s\n' "$name" "optional-missing"
  fi
}

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

printf '%-30s %s\n' "check" "result"
printf '%-30s %s\n' "uid" "$(id -u)"
printf '%-30s %s\n' "kernel" "$(uname -r)"
check_optional "go" go version
check_optional "protoc" protoc --version
check_optional "buf" buf --version
check_optional "controller-gen" controller-gen --version
check_required "lvs" lvs --version
check_required "vgs" vgs --version
check_required "pvs" pvs --version
check_optional "iscsiadm" iscsiadm --version
check_optional "nvme" nvme version
check_required "tlshd" sh -c 'command -v tlshd'
check_optional "targetcli" targetcli --version
check_optional "nvmetcli" nvmetcli --help

if [ -d /sys/kernel/config/target ]; then
  printf '%-30s %s\n' "lio configfs" "ok"
else
  printf '%-30s %s\n' "lio configfs" "missing"
  fail=1
fi

if [ -d /sys/kernel/config/nvmet ]; then
  printf '%-30s %s\n' "nvmet configfs" "ok"
else
  printf '%-30s %s\n' "nvmet configfs" "missing"
  fail=1
fi

for module in target_core_mod target_core_iblock iscsi_target_mod nvmet nvmet_tcp; do
  if lsmod 2>/dev/null | grep -q "^${module} "; then
    printf '%-30s %s\n' "module ${module}" "ok"
  else
    printf '%-30s %s\n' "module ${module}" "missing"
    fail=1
  fi
done

if [ "$(id -u)" -ne 0 ]; then
  printf '%-30s %s\n' "root privileges" "missing"
  fail=1
else
  printf '%-30s %s\n' "root privileges" "ok"
fi

if kernel_config_enabled CONFIG_NVME_TARGET_TCP_TLS; then
  printf '%-30s %s\n' "CONFIG_NVME_TARGET_TCP_TLS" "ok"
else
  printf '%-30s %s\n' "CONFIG_NVME_TARGET_TCP_TLS" "missing"
  fail=1
fi
if kernel_config_enabled CONFIG_NVME_TARGET_AUTH; then
  printf '%-30s %s\n' "CONFIG_NVME_TARGET_AUTH" "ok"
else
  printf '%-30s %s\n' "CONFIG_NVME_TARGET_AUTH" "missing"
  fail=1
fi
if kernel_config_enabled CONFIG_NVME_TCP_TLS; then
  printf '%-30s %s\n' "CONFIG_NVME_TCP_TLS" "ok"
else
  printf '%-30s %s\n' "CONFIG_NVME_TCP_TLS" "optional-missing"
fi

if systemctl is-active --quiet tlshd 2>/dev/null; then
  printf '%-30s %s\n' "tlshd service" "active"
else
  printf '%-30s %s\n' "tlshd service" "missing"
  fail=1
fi

if [ -d /sys/kernel/debug/nvmet ] || [ -d /sys/kernel/config/nvmet/subsystems ]; then
  if [ -d /sys/kernel/debug/nvmet ]; then
    printf '%-30s %s\n' "nvmet session interface" "debugfs"
  elif [ -d /sys/kernel/config/nvmet/subsystems ]; then
    printf '%-30s %s\n' "nvmet session interface" "unknown (no controllers group)"
  fi
fi

if [ "$fail" -ne 0 ]; then
  printf '\nPhase 0 storage host probe failed. Fix the required items before Stage 1 adapter work.\n'
  exit 1
fi

printf '\nPhase 0 storage host probe passed.\n'

if ! command -v go >/dev/null 2>&1 || ! command -v buf >/dev/null 2>&1 || ! command -v controller-gen >/dev/null 2>&1; then
  printf 'Build-host tooling is incomplete; this is expected on a runtime-only storage host.\n'
fi
