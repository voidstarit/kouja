# syntax=docker/dockerfile:1

# Multi-stage build for the kouja command suite.
#
# The image contains every executable built from ./cmd/... (agent, backup,
# restore, CSI controller/node, and the Terraform provider). The agent and CSI
# node are host-level services: they manage LVM volume groups, write LIO and
# nvmet configfs trees, and run as root. The runtime image therefore includes
# the client tooling those commands shell out to, but it still needs the host's
# kernel modules, configfs, and block devices to do useful work.

ARG ALPINE_VERSION=3.24
ARG GO_VERSION=1.26
ARG NODE_VERSION=22

# ---------------------------------------------------------------------------
# Build the Preact admin UI that is embedded into the agent binary.
# ---------------------------------------------------------------------------
FROM node:${NODE_VERSION}-alpine AS ui

WORKDIR /src

COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci

COPY web ./web
RUN npm --prefix web run build

# ---------------------------------------------------------------------------
# Compile every command statically so the runtime image needs no toolchain.
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build

RUN apk add --no-cache ca-certificates git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The UI stage writes to internal/adminui/assets, which assets.go embeds.
# Copy it after the source so the freshly built bundle always wins.
COPY --from=ui /src/internal/adminui/assets/ /src/internal/adminui/assets/

# Build all commands under ./cmd, matching the release Makefile: the agent and
# Terraform provider receive the build version through ldflags.
ARG VERSION=dev
RUN set -eux; \
    for package in $(go list ./cmd/...); do \
        name="$(basename "${package}")"; \
        extra=""; \
        case "${name}" in \
            kouja-agent) extra="-X main.agentVersion=${VERSION}" ;; \
            terraform-provider-kouja) extra="-X main.version=${VERSION}" ;; \
        esac; \
        CGO_ENABLED=0 GOOS=linux go build -trimpath \
            -ldflags "-s -w ${extra}" \
            -o "/out/${name}" "${package}"; \
    done

# ---------------------------------------------------------------------------
# Runtime image.
# ---------------------------------------------------------------------------
FROM alpine:${ALPINE_VERSION}

# lvm2 provides the LVM report/scan/display tools (pvs, vgs, lvs, pvscan,
# vgscan, lvscan, pvdisplay, vgdisplay, lvdisplay, lvmdiskscan) and the
# readiness-checked binaries; device-mapper adds dmsetup and dmstats; eudev
# provides udevadm; util-linux provides findmnt, blockdev, mountpoint, lsblk,
# and blkid; nvme-cli generates NVMe/TCP TLS keys; open-iscsi and the
# filesystem tools are required by the CSI node plugin; e2fsprogs-extra and
# xfsprogs-extra add resize2fs/xfs_growfs.
# Upgrade the base package set first so security fixes (for example zlib) land
# even when the pinned Alpine tag's cached index predates them.
RUN apk upgrade --no-cache \
    && apk add --no-cache \
        ca-certificates \
        device-mapper \
        e2fsprogs \
        e2fsprogs-extra \
        eudev \
        lvm2 \
        nvme-cli \
        open-iscsi \
        tzdata \
        util-linux \
        xfsprogs \
        xfsprogs-extra \
    && mkdir -p /etc/kouja /var/lib/kouja \
    && chmod 0700 /etc/kouja /var/lib/kouja

COPY --from=build /out/ /usr/local/bin/
COPY deploy/kubernetes/csi/entrypoint-node.sh /usr/local/bin/entrypoint-node.sh
COPY deploy/config.example.yaml /usr/share/kouja/config.example.yaml
RUN chmod 0755 /usr/local/bin/*

# Persistent state: SQLite database, host ID, secret key, and backups.
VOLUME ["/var/lib/kouja"]

# 9443 control API (mTLS), 9444 local admin UI, 3260 iSCSI, 4420 NVMe/TCP,
# 9100 optional plaintext metrics.
EXPOSE 9443 9444 3260 4420 9100

STOPSIGNAL SIGTERM

# The agent and CSI node must run as root to manage LVM, configfs, and mounts.
USER root

# No ENTRYPOINT keeps the image usable for every command:
#   docker run --rm IMAGE kouja-backup -config /etc/kouja/config.yaml
# The default command is the agent.
CMD ["/usr/local/bin/kouja-agent", "-config", "/etc/kouja/config.yaml"]
