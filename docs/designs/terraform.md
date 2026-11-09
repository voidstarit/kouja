# Terraform Provider for kouja Administration

*   **Author:** kouja maintainers
*   **Status:** Draft
*   **Date:** 2026-11-08

--------------------------------------------------------------------------------

## Overview

kouja is a per-host block-storage agent. Each storage host runs its own
`kouja-agent`, its own SQLite database, and its own LVM and target
configuration. The agent exposes an mTLS gRPC and HTTP/JSON API. A client
certificate with the `admin` role can register volume groups, import volumes,
and perform other administrative actions.

Today the three setup steps that precede volume provisioning are split across
different interfaces:

*   Volume groups can be registered through the mTLS admin API or seeded from
    the configuration file.
*   Storage classes can be changed only through the loopback-only local admin
    UI or by editing `storage.classes` and restarting the agent.
*   Client certificates are issued by the loopback-only local admin UI, by an
    external CA, or by the installer helper. The admin UI can create
    certificate files but cannot delete or revoke them.

This design targets the `main` branch at commit `9889e3c`, which includes the
local admin UI, its certificate-issuance path, and the live storage-class
registry (merged in PR #8). It adds the missing administrative operations to
the mTLS admin API and adds a Terraform provider that uses that API with an
admin certificate. The provider is built from
`cmd/terraform-provider-kouja` in the kouja repository. It manages three
resource types: client certificates, volume group registrations, and
storage-class mappings. It does not manage volumes, exports, or the CSI
driver.

The certificate model uses server-generated keypairs with durable issuance
records and revocation. The agent signs a certificate with its configured
client CA, writes the certificate and private key files, records the serial
number and metadata in SQLite, and returns the keypair to the caller. Issuance
is not idempotent: a lost response leaves an orphan that is listed and revoked
manually. Revocation appends to an append-only journal, updates SQLite and the
in-memory cache, removes the files, and makes the agent reject the certificate
on every subsequent mTLS request. The journal keeps revocation effective after
a restart or an older-database restore.

This model puts the private key on the agent and in Terraform state. The
design documents the exposure and requires Terraform state to be treated as a
credential store. Any admin certificate can issue new admin, caller,
controller, or node certificates. A compromised admin is equivalent to a CA
compromise; audit rows enable detection and attribution, and the TTL bounds
the exposure window. The design records `issued_by`, requires a long-lived
bootstrap certificate, and requires CA rotation on suspected admin compromise.

## The Problem

An operator who wants to add a storage host to a Terraform-managed environment
must perform setup through three mechanisms:

1.  Register each LVM volume group through the mTLS API, the installer
    configuration, or the local UI.
2.  Create storage-class mappings through the unauthenticated loopback UI or
    by editing the agent YAML and restarting.
3.  Issue caller, controller, node, and admin certificates through the local
    UI, an external CA, or manual commands, then distribute the files.

This creates five problems:

*   **No single declarative source of truth.** Terraform cannot describe the
    complete host setup. A host rebuilt from Terraform still needs manual UI or
    configuration steps. Two hosts configured from the same Terraform code can
    end up with different class maps or certificate files.
*   **Remote automation depends on a local browser UI.** The admin UI listens
    only on loopback and has no authentication. Using it from CI requires SSH
    tunneling or a jump host. The UI's trust model assumes a
    single-administrator host with no untrusted local users. That model does not
    support automated provisioning.
*   **Certificate issuance has no queryable registry or durable revocation
    record.** The current admin UI writes `.crt` and `.key` files and lists the
    directory. It cannot tell whether a certificate is still valid, cannot list
    issued certificates from the API, and cannot revoke one. Deleting a file
    does not stop a copied certificate from authenticating.
*   **Storage-class changes are not available to remote administrators.**
    Classes drive volume placement and capacity selection. They can be changed
    only on the host, so a Terraform workflow cannot create or update them.
*   **Volume-group behavior differs between the two admin surfaces.** The mTLS
    registration path passes `replace=true`, so it can rewrite an existing
    VG's UUID, while the local UI refuses the replacement. Terraform needs a
    non-destructive contract, and both paths need the same audit trail.

The result is that Terraform cannot manage the host's identity, credentials,
VGs, or classes. Those resources remain hand-managed, which makes replacement,
audit, and disaster recovery slower and more error-prone.

## Goals

*   **G1: Certificate issuance over mTLS.** Add an admin RPC that issues a
    server-generated client keypair for the roles `admin`, `caller`,
    `controller`, and `node`. The request carries the role, name, and optional
    time-to-live. `0` means the configured default (`default_ttl_days`, 30
    unless overridden); the maximum is `max_ttl_days`, 365 unless overridden. The agent validates role, name, and TTL, signs the certificate with
    the configured client CA, and returns the certificate PEM, private key PEM,
    client CA PEM, serial number, and validity window. Issuance is not
    idempotent; `request_id` is audit correlation only.
*   **G2: Durable certificate records and revocation.** Store every issued
    certificate's serial number, role, name, issuer, request ID, issuance time,
    expiry time, and revocation state in SQLite. Append revocations to a
    separate journal. Both must survive an agent restart and be included in the
    host backup. Rows and journal entries are pruned after expiry.
*   **G3: Certificate listing and revocation.** Add paginated admin RPCs to
    list issued certificates and to revoke one by serial number. Revocation
    appends to the journal, updates SQLite and the cache, removes the
    certificate and key files, and is idempotent. A missing file after a
    committed revocation is a warning, not an error.
*   **G4: Revocation enforcement.** Reject a revoked client certificate on
    every gRPC and HTTP/JSON request through the mTLS listener, including all
    four roles and health/metrics. The cache is the union of revoked,
    unexpired serials from the journal and SQLite; the journal is the
    durability source and SQLite is the metadata source. The agent loads revoked, unexpired
    serials from both at startup and updates the in-memory set synchronously
    inside the revoke operation. A successful revoke response means the current
    process rejects the certificate on the next request. The behavior is tested
    across restart, older-database restore, and both transports.
*   **G5: Local UI certificate management.** The local admin UI lists registry
    rows, shows revocation state, and provides create, download, and revoke
    actions. It does not present unmanaged files as revocable and never exposes
    the CA private key. The UI remains loopback-only and unauthenticated.
*   **G6: Storage-class administration over mTLS.** Add admin RPCs to list,
    get, set, and remove storage-class mappings. The HTTP surface uses one
    mapping per path: `GET /v1/admin/classes`,
    `GET /v1/admin/classes/{class_name}`,
    `PUT /v1/admin/classes/{class_name}`, and
    `DELETE /v1/admin/classes/{class_name}`. Writes must use the same live
    `ClassRegistry` and configuration persistence path as the local UI, must
    check that the target VG is registered, must surface persistence warnings,
    and must enforce the existing rule that one VG maps to at most one class.
*   **G7: Non-destructive VG administration.** Keep the shared VG service, but
    change the mTLS registration path to `replace=false`. A request-supplied
    UUID mismatch returns `VG_UUID_MISMATCH` with `FailedPrecondition`; a
    stored-vs-observed mismatch returns `VG_ALREADY_EXISTS` with
    `AlreadyExists`. Deregistration still fails when a VG has volumes or a
    class mapping. Both the mTLS and local UI paths write audit rows through
    the shared recorder.
*   **G8: Terraform resources.** Provide a Terraform provider with an
    entrypoint at `cmd/terraform-provider-kouja` and implementation under
    `internal/terraformprovider`. It exposes resources
    `kouja_client_certificate`, `kouja_volume_group`, and
    `kouja_storage_class`, and uses the shared `gen/client` package for the
    admin API. Each resource must support create, read, update where the agent
    permits it, and delete. Certificate destroy must revoke. VG create surfaces
    `VG_UUID_MISMATCH` or `VG_ALREADY_EXISTS`; VG destroy surfaces `VG_IN_USE`
    or `VG_CLASS_MAPPED`; class destroy treats `NotFound` as success. Refusals
    are typed diagnostics, not silent success.
*   **G9: Terraform authentication.** The provider must authenticate with a
    client certificate that has the `admin` role. Configuration must support
    inline PEM values and file paths, a custom CA bundle, and a server name
    override for TLS verification.
*   **G10: Private key handling.** The provider marks private key attributes
    as sensitive, never logs them, and rejects an incomplete keypair response
    (missing certificate PEM, private key PEM, client CA PEM, or serial).
    Documentation states that Terraform state contains private keys and that
    protecting the state backend is the operator's responsibility. The design
    does not add an ephemeral-resource or write-only-key mode.
*   **G11: Verification.** Unit tests cover the new agent services and
    revocation checks. An mTLS integration test proves that an issued
    certificate works, a revoked certificate is rejected, and a non-admin
    certificate cannot call the new admin RPCs. Terraform acceptance tests
    cover every resource lifecycle against a controlled test server.
*   **G12: Compatibility.** Existing volume, export, credential, and CSI
    clients are unaffected. A host without `certificate_authority` keeps
    issuance disabled and returns `CERTIFICATE_ISSUANCE_DISABLED`. The VG
    registration contract changes to `replace=false`; the old `admin_ui`
    certificate fields are removed without an alias; the local UI TTL changes
    to the configured default/maximum (30/365 unless overridden); certificate
    file names gain the serial suffix. `docs/upgrading.md`
    lists every breaking change.
*   **G13: Audit.** Use one shared `internal/audit.Recorder` for every
    certificate issuance, revocation, class set/remove, and VG
    register/deregister. The row includes caller, operation ID, request ID,
    resource type/ID, role, name, result, and error code. Issuance rows record
    the serial and role but never the private key or certificate PEM.

## Non-Goals

*   **Volume and export lifecycle.** The provider does not create, resize,
    export, unexport, or delete volumes. That remains the CSI driver's job or a
    direct API caller's job.
*   **CSI driver changes.** No Kubernetes StorageClass, PV, or CSI identity
    behavior changes.
*   **Volume group creation.** Terraform registers an existing LVM volume
    group. It does not run `vgcreate`, partition disks, or install LVM.
*   **Certificate authority lifecycle.** The design does not create, rotate, or
    distribute the client CA. It uses the CA files the operator already
    installed. Automated fleet-wide rotation with trust-bundle overlap is a
    separate design; the manual no-overlap runbook is documented.
*   **External revocation distribution.** The agent enforces revocation for its
    own listener. It does not publish a CRL or run OCSP for other services.
    Certificates signed by a different CA, or whose PEM is unavailable, cannot
    be verified or revoked through this API; client CA rotation is the
    fallback. Certificates signed by the current client CA can be revoked with
    `RevokeExternalCertificate` when their PEM is available.
*   **Multiple classes per VG.** The existing one-class-per-VG rule remains.
*   **Private key recovery or escrow.** Once the certificate files are deleted
    and Terraform state is lost, the keypair cannot be recovered. There is no
    escrow service.
*   **Importing existing certificates.** A certificate issued outside kouja
    cannot be adopted into the `kouja_client_certificate` resource because the
    agent has no issuance record and the provider has no private key.
*   **Volume placement or host selection.** The provider targets one agent
    endpoint. Choosing which host receives a volume remains outside this
    provider.
*   **Replacing Terraform state security.** The provider cannot make an
    unencrypted local state file safe. Operators must use an encrypted remote
    backend or another secret-handling mechanism.
*   **Public Terraform Registry publication.** A public registry provider needs
    a repository named `terraform-provider-kouja` at its root. This design
    builds the provider from the kouja repository at
    `cmd/terraform-provider-kouja`; operators install release binaries, a
    private registry, or a filesystem mirror. A separate mirror repository is
    future work.
*   **Configuration migration for the old admin UI certificate paths.**
    `admin_ui.client_cert_dir` and `admin_ui.client_ca_key_file` are replaced by
    the new `certificate_authority` section without an alias. The feature is
    not deployed in production, so no migration path is implemented.
*   **Managing the provider's own bootstrap credential.** The admin certificate
    the provider uses to authenticate is issued outside Terraform. The
    provider refuses to revoke the certificate it is currently using, so a
    single apply cannot lock itself out.

## Design Decisions

These choices were resolved during drafting and are part of the design:

*   **Baseline.** `main` at commit `9889e3c`, which includes PR #8.
*   **Provider location.** The provider entrypoint is
    `cmd/terraform-provider-kouja/main.go`; implementation lives under
    `internal/terraformprovider`. The provider is part of the existing Go
    module and uses `gen/client`. The Terraform SDK is a provider-only dependency and is not linked into
    the agent binary. Public registry publication is a
    non-goal.
*   **Certificate time-to-live.** The agent default is `default_ttl_days` (30
    unless overridden); the maximum is `max_ttl_days` (365 unless overridden). Terraform omits `ttl_days` when it is not configured, so the
    agent default applies. A set Terraform value overrides the default for that
    certificate. Negative values are invalid.
*   **Issuance idempotency.** Issuance is not idempotent. `request_id` is an
    audit correlation ID. A second call creates a new certificate. A lost
    response leaves an orphan that is listed and revoked manually. The provider
    does not retry `IssueCertificate`.
*   **Revocation durability.** Revocation appends to an append-only journal
    outside SQLite, then updates the in-memory cache, then updates the SQLite
    row and audit row. The journal makes revocation survive a restart and a
    restore of an older database. SQLite is the queryable metadata source.
*   **Audit.** Schema version 2 adds explicit audit fields for resource type,
    resource ID, role, name, and detail. A shared `internal/audit.Recorder` is
    the only writer used by the certificate authority, the admin service, and
    the local UI. Certificate and VG audit rows share the mutation transaction;
    class audit failures return an `audit_write_failed` warning because the
    YAML write is already durable.
*   **CA configuration.** A new `certificate_authority` section owns
    `enabled`, `key_file`, `certificate_dir`, `revocation_log_file`,
    `default_ttl_days`, `max_ttl_days`, and `max_active_certificates`. There
    is no alias for the old `admin_ui.client_cert_dir` and
    `admin_ui.client_ca_key_file` fields.
*   **CA availability.** A `certificate_authority.key_file` or
    `certificate_dir` failure puts issuance in degraded mode; it does not stop
    the volume service. `Issue` returns `CERTIFICATE_CA_UNAVAILABLE`;
    list/revoke and storage continue. A missing, invalid, or expired
    `listen.tls.client_ca_file` is a startup error, not degraded issuance.
*   **Certificate file naming.** Issued files are named
    `<role>-<name>-<serial>.crt` and `.key`. The serial suffix lets two
    certificates with the same role and name coexist during replacement, so
    `create_before_destroy` works without overwriting a live credential.
*   **Class API shape.** The admin API exposes one mapping per path:
    `GET /v1/admin/classes`, `GET /v1/admin/classes/{class_name}`,
    `PUT /v1/admin/classes/{class_name}`, and
    `DELETE /v1/admin/classes/{class_name}`.
*   **VG registration and read.** The mTLS API registers with `replace=false`.
    A request UUID mismatch is `VG_UUID_MISMATCH`; a stored/observed mismatch
    is `VG_ALREADY_EXISTS`. `GetVG` returns the stored registration and the
    observed capacity separately; the provider warns on UUID drift.
*   **Certificate record retention.** Rows and journal entries are kept until
    `not_after`, then pruned and compacted. Expired serials are removed from
    the cache at the same time.
*   **Terraform state.** The provider marks private keys sensitive. State
    security is the operator's responsibility. The design does not add an
    ephemeral-resource or write-only-key mode.
*   **Provider self-revocation and bootstrap.** The provider refuses to revoke
    the certificate it is currently using. The bootstrap admin certificate is
    issued outside Terraform, uses the maximum TTL, and is rotated before
    expiry. Admin compromise is treated as CA compromise.
*   **UI scope.** The local UI lists registry rows and provides create,
    download, and revoke. It does not add a separate unmanaged-files table, a
    TTL editor, or a new badge component. The UI is loopback-only and
    unauthenticated.
*   **Provider acceptance.** Automated acceptance uses an `httptest` TLS server
    with `terraform-plugin-testing`; the real-agent check is the manual QEMU
    procedure in `docs/e2e.md`.
*   **Unmanaged certificates.** Certificates issued before this feature or
    outside the registry can be revoked with `RevokeExternalCertificate` when
    their PEM is available and signed by the current client CA. Certificates
    without a usable PEM, or signed by a rotated CA, require client CA rotation
    or host reprovisioning. The agent exposes an unmanaged-file metric.

## Open Questions

None at this stage.

## Critical User Journeys (CUJs)

### CUJ 1: Bootstrap a storage host for Terraform management

*   **Given**: kouja is installed on a host with a valid server certificate,
    a client CA certificate, a CA private key, and an existing LVM volume
    group. The host is reachable over the network.
*   **When**: The operator installs the CA key, sets
    `certificate_authority.enabled: true`, restarts the agent, issues a
    bootstrap admin certificate with `ttl_days = max_ttl_days` (365 by default)
    through the operator's CA or the admin API, and configures the Terraform
    provider with that certificate. The local UI create form is not the
    long-lived bootstrap path; it uses the configured default.
*   **Then**: The provider's `Configure` call succeeds; `GetInfo` capabilities
    include `certificates` and `storage_classes`; `GetInfo.caller_role` is
    `admin`; no managed resources exist yet.

### CUJ 2: Declare a volume group and storage class

*   **Given**: The provider is configured with an admin certificate. The LVM
    volume group `kouja-nvme` exists on the host and is not registered.
*   **When**: The operator applies a `kouja_volume_group` named `kouja-nvme`
    and a dependent `kouja_storage_class` named `nvme` with `vg =
    kouja_volume_group.kouja-nvme.name`.
*   **Then**: `GetVG` shows the stored UUID, observed UUID, and capacity;
    `GetClass` returns the mapping from `nvme` to `kouja-nvme`; the local UI
    shows both; destroy removes the class before the VG. A VG destroy while a
    class references it fails with `VG_CLASS_MAPPED`; a destroy while volumes
    exist fails with `VG_IN_USE`.

### CUJ 3: Issue, use, and rotate a caller certificate

*   **Given**: The provider is configured and the `rookery-prod` caller name is
    available.
*   **When**: The operator applies a `kouja_client_certificate` with role
    `caller`, name `rookery-prod`, and `ttl_days = 30`, uses the returned
    certificate and key to call `GetInfo`, then changes the TTL or name with
    `create_before_destroy`.
*   **Then**: The first certificate works until it is revoked; the replacement
    is a new issue with a new serial and new file names; the old certificate is
    revoked after the replacement; Terraform state contains only the new
    private key.

### CUJ 4: Revoke a compromised certificate

*   **Given**: A caller certificate was issued through Terraform or the local
    UI and is being used by a client.
*   **When**: The operator runs `terraform destroy` for the certificate, uses
    the local UI's revoke action, or calls `RevokeCertificate`.
*   **Then**: The journal append succeeds, the cache is updated before the
    response, the SQLite row is marked revoked, the files are deleted, the next
    request returns `CERTIFICATE_REVOKED`, and the rejection survives an agent
    restart and an older-database restore.

### CUJ 5: Local UI operator manages a certificate

*   **Given**: The local admin UI is enabled, `certificate_authority` is
    enabled, and the operator can reach the loopback listener.
*   **When**: The operator lists registry rows, downloads the CA, issues a new
    certificate, and revokes an existing one.
*   **Then**: The list shows active and revoked registry rows; the CA private
    key is never listed or downloadable; revocation is enforced by the mTLS
    listener on the next request. The UI does not present unmanaged files as
    revocable.

--------------------------------------------------------------------------------

## Technical Plan (Architecture)

### 1. System Shape

The provider is a thin HTTP/JSON client. It does not touch LVM, the agent
configuration file, or the certificate directory. Every state change goes
through the agent's mTLS admin API. The agent remains the only component that
knows how to sign certificates, register VGs, or persist class mappings.

The local admin UI and the mTLS admin API share the same service objects. That
is the primary control against the current behavior split: both surfaces call one
certificate authority, one VG service, and one class registry. A change made
through Terraform is visible in the UI, and a change made in the UI is visible
to the provider.

```text
  Terraform CLI
       │
       ▼
  terraform-provider-kouja            (cmd/terraform-provider-kouja)
       │  gen/client — HTTP/JSON, mTLS, admin certificate
       ▼
  kouja-agent :9443
       │
       ├── TLS 1.3 listener
       │     ├── identity from URI SAN
       │     └── revocation check (in-memory set from journal + SQLite)
       │
       ├── VolumeService
       │     ├── volume lifecycle (unchanged)
       │     └── GetInfo + capabilities
       │
       └── AdminService
             ├── VG/Class RPCs  ──► agent.VGClassManager ──► SQLite + YAML
             └── Certificate RPCs
                       │
                       ▼
                 internal/pki.Service
                       ├── client CA certificate + private key
                       ├── issued certificate files (certificate_dir)
                       ├── SQLite certificate registry
                       ├── append-only revocation journal
                       └── revocation cache

  Local admin UI :9444 ──► agent.VGClassManager (VGService/ClassRegistry)
                           └── pki.Service
```

The agent's SQLite database is the authority for VGs and certificate
metadata. The append-only revocation journal is the authority for enforcement;
SQLite is the queryable metadata source. The YAML configuration is the durable
store for class mappings, with the live `ClassRegistry` holding the in-memory
copy. Certificate files are a delivery and download format, not the source of
truth for whether a certificate was issued or revoked.

### 2. New Certificate Authority Component

#### 2.1 Responsibilities and interfaces

A new package, `internal/pki`, owns certificate issuance, the registry, file
layout, the revocation journal, and the revocation cache. It must not import
`internal/agent` or `internal/adminui`. It may import `internal/audit`, `internal/config`, `internal/fsutil`,
`internal/naming`, `internal/state`, `internal/warnings`, and
`internal/observability`.

The package exposes narrow interfaces at the consumer boundaries:

```text
Issuer
  Issue(ctx, Caller, IssueRequest) (IssuedCertificate, error)

Registry
  Get(ctx, serial) (CertificateRecord, error)
  List(ctx, ListOptions) (CertificatePage, error)
  Revoke(ctx, Caller, serial, reason) (CertificateRecord, []warnings.Warning, error)

FileStore
  ListFiles(ctx) ([]FileInfo, error)
  ResolveFile(name) (string, error)

ExternalRevoker
  RevokeExternal(ctx, Caller, certificatePEM, reason) (CertificateRecord, []warnings.Warning, error)

RevocationChecker
  IsRevoked(serial string) bool

CAStatus
  Ready() bool
  CAState() error
```

`Service` is the concrete implementation of all six interfaces. Its concrete
fields include `issueMu`, `revokeMu`, and `journalMu sync.Mutex`.
`New(Options) (*Service, error)` constructs it. `Service.Run(ctx) error` runs
an immediate cleanup, then an hourly ticker, and returns when the context ends.
`Options` fields:

```text
Options
  Config  *config.Config
  Store   *state.Store
  Audit   *audit.Recorder
  Logger  *slog.Logger
  Now     func() time.Time
  Metrics *observability.Metrics
  Naming  *naming.Validators
```

`internal/naming.Validators` exposes per-kind methods rather than one combined
regex:

```text
naming.Validators
  CertificateName(name string) error
  VolumeGroupName(name string) error
  StorageClassName(name string) error

NewValidators() *Validators
```

Certificate names use `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`; VG and class names
use `^[A-Za-z0-9+_.-]{1,127}$`. The two regexes are intentionally distinct.

Types and fields:

```text
Caller
  Owner, SerialNumber, RequestID, Source, RemoteAddr string

IssueRequest
  Role, Name string
  TTLDays    int32   // 0 means the configured default

IssuedCertificate
  CertificatePEM, PrivateKeyPEM, ClientCAPEM string
  SerialNumber, Role, Name, IssuedBy         string
  IssuerSerial, RequestID                    string
  NotBefore, NotAfter                        time.Time
  Warnings                                   []warnings.Warning

CertificateRecord
  SerialNumber, Role, Name, IssuedBy        string
  IssuerSerial, RequestID                   string
  CertificatePEM, ClientCAPEM               string
  CertFile, KeyFile                         string
  NotBefore, NotAfter, CreatedAt             time.Time
  RevokedAt *time.Time
  RevokedReason string

CertificateSummary
  SerialNumber, Role, Name, IssuedBy        string
  IssuerSerial                              string
  NotBefore, NotAfter, CreatedAt             time.Time
  RevokedAt *time.Time
  Revoked bool

ListOptions
  PageSize  int32
  PageToken string

CertificatePage
  Certificates  []CertificateSummary
  NextPageToken string

FileInfo
  Name, Role, Kind, SerialNumber string
  SizeBytes                      int64
  ModifiedAt                     time.Time
  Managed, Revoked               bool
  NotAfter                       time.Time
```

The registry is paginated: `PageSize` defaults to 50 and is capped at 100.
The list is sorted by creation time descending, then by serial.

The shared audit recorder is `internal/audit.Recorder`. The authority does not
own a second audit implementation. The two admin surfaces pass the same
recorder through `Options`.

#### 2.2 Startup, CA loading, and degraded mode

The authority is constructed during agent startup, before the control listener
and the admin UI. It receives the parsed configuration, the state store, the
audit recorder, the logger, and metrics. `New` has explicit branches:

*   A missing, invalid, or expired `listen.tls.client_ca_file`, invalid
    configuration, an unreadable state store, or an unreadable journal returns
    a startup error. The mTLS listener does not start without a usable client
    CA.
*   A `certificate_authority.key_file` or `certificate_dir` failure when
    issuance is enabled returns a non-nil `*Service` and nil error. The service
    exposes `Ready() false` and `CAState()` with the error; `main` logs it and
    sets `kouja_certificate_authority_ready=0`.
*   Disabled issuance does not load the CA key and does not treat a missing CA
    key as an error.

When `certificate_authority.enabled` is true, construction attempts to:

1.  Read the client CA certificate from `listen.tls.client_ca_file`.
2.  Read the CA private key from `certificate_authority.key_file`.
3.  Parse the PEM blocks. Accept PKCS#1 RSA, SEC1 EC, and PKCS#8 keys. Reject
    encrypted keys.
4.  Verify that the key matches the certificate's public key.
5.  Verify that the certificate has `IsCA`, `BasicConstraintsValid`, is
    currently valid, and has `KeyUsageCertSign` when `KeyUsage` is present.
    Accept RSA keys of at least 2048 bits, ECDSA P-256/P-384, and Ed25519.
6.  Verify the key file is a regular file, mode `0600` or stricter, owned by
    the agent user.
7.  Create the certificate directory with `0700` if it is missing, then verify
    it is an absolute regular directory, mode `0700`, owned by the agent user,
    and not the same directory as the CA key, server key, or client CA file.
8.  Load revoked, unexpired serials from the revocation journal and SQLite
    into the revocation cache.

If a `certificate_authority.key_file` or `certificate_dir` check fails while
issuance is enabled, the agent does **not** stop the volume service. It starts
in **degraded issuance mode**:

*   `Issue` returns `CERTIFICATE_CA_UNAVAILABLE`.
*   `List`, `Get`, `Revoke`, `IsRevoked`, `Run`, and `ListFiles` continue to
    work for records that are already present.
*   The agent logs a hard error and sets
    `kouja_certificate_authority_ready` to 0.
*   The local UI reads `pki.CAStatus`, shows `CAState()` as the error, and
    disables certificate creation. `issuance_enabled` is
    `config.Enabled && CAStatus.Ready()`; disabled issuance still allows list
    and revoke.

This is fail-closed for credential issuance and fail-open for block storage.
The degraded scope is limited to `certificate_authority.key_file` and
`certificate_dir`. A missing, invalid, or expired `listen.tls.client_ca_file`
is a hard mTLS failure, not a degraded-issuance condition.

When `certificate_authority.enabled` is false, no CA key is required. `Issue`
returns `CERTIFICATE_ISSUANCE_DISABLED`. `List`, `Get`, `Revoke`,
`IsRevoked`, `Run`, and the journal/cache path still work. The UI lists
registry rows only; it does not list certificate-directory files.

The CA certificate's `NotAfter` is the hard upper bound. The authority never
issues a certificate that outlives the CA.

#### 2.3 Issue flow

Issuance is **not idempotent**. Every successful call creates a new
certificate. `request_id` is an audit correlation ID, not a replay key. The
provider must not retry `IssueCertificate`; a lost response leaves an orphan
that is listed and revoked manually.

1.  Validate the role (`admin`, `caller`, `controller`, `node`) and the name
    against `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`.
2.  Resolve the time-to-live. `0` means the configured default. A negative
    value or a value larger than `max_ttl_days` is rejected. Require
    `CA.NotAfter > now + 5m`; otherwise return `CERTIFICATE_CA_UNAVAILABLE`,
    set `Ready()` false, set `CAState()` to expired, and set
    `kouja_certificate_authority_ready=0`. The final `NotAfter` is
    `min(now + ttl, CA.NotAfter)`.
3.  Resolve the request ID. If the caller did not supply one, generate a UUID.
    The ID is written to the audit row and returned in the issue response.
    There is no lookup or replay path.
4.  Serialize issuance with the service issue mutex. Count active rows
    (`revoked_at IS NULL` and `not_after > now`). If the count is at or above
    `max_active_certificates`, release the mutex and return
    `CERTIFICATE_QUOTA_EXCEEDED` with `ResourceExhausted`.
5.  Generate an ECDSA P-256 private key and a positive random 128-bit serial
    number. Encode it as lowercase hexadecimal from `big.Int.Text(16)` with no
    leading zero bytes. Retry on a primary-key collision. The accepted serial
    format is a positive `big.Int`, lowercase hex, no leading zeros, and 1–40
    hex characters; generated serials are up to 32 characters and are never
    zero-padded. External serials are
    normalized by lowercasing and stripping leading zeros before lookup,
    insert, cache, and comparison. Zero, non-hex, or over-40-character values
    are invalid.
6.  Build an X.509 template with:
    *   subject common name `kouja-<role>-<name>`;
    *   URI SAN `spiffe://kouja/<role>/<name>`;
    *   `KeyUsageDigitalSignature`;
    *   `ExtKeyUsageClientAuth`;
    *   `NotBefore` five minutes in the past;
    *   `NotAfter` from step 2.
7.  Sign with the CA.
8.  Write `<role>-<name>-<serial>.crt` and `<role>-<name>-<serial>.key` with
    the exclusive atomic writer from `internal/fsutil`. Certificate files are
    mode `0644`; key files are mode `0600`; no existing file is overwritten.
9.  In one SQLite transaction, insert the certificate row and the
    `certificate_issue` audit row with result `succeeded`. The row stores
    `issued_by = Caller.Owner`, `issuer_serial = Caller.SerialNumber`, and
    `client_ca_pem = the current client CA PEM`. The private key is never
    written to SQLite. If the transaction fails, remove the files, write a
    best-effort `failed` audit row, and return an error.
10. Return the certificate PEM, private key PEM, client CA PEM, serial,
    validity window, role, name, request ID, issuer serial, and any file-sync
    warnings.

A file write that returns `fsutil.ErrDurabilityUncertain` is treated as
success with a `durability_uncertain` warning; the registry row is kept and the
warning is returned to the caller. The issue API is the only API response that
returns the private key. It is never returned by `GetCertificate` or
`ListCertificates`. The loopback UI may download the `0600` key file under its
root-equivalent trust model. A second issue call creates a new key and serial.

The serial is the Terraform resource ID and the API key for read and revoke.

Certificate roles:

| Role | Permissions |
|---|---|
| `admin` | All admin RPCs: certificate issue/list/revoke, VG registration, class mapping, import, drift resolution, wipe, transfer, and admin delete. |
| `caller` | Create, export, unexport, resize, delete, and read volumes it owns; read its own credential. |
| `controller` | CSI controller operations on volumes it owns; grant and revoke credentials for its volumes. |
| `node` | Fetch a credential only with a valid grant for that volume; node-scoped operations. |

The full ownership and authorization matrix is in `docs/designs/kouja.md`.

#### 2.4 Revoke flow

1.  Acquire the service `revokeMu` for the rest of the operation, then
    validate and normalize the serial format (lowercase hex, no leading zeros,
    1–40 characters). A malformed serial returns `CERTIFICATE_SERIAL_INVALID`.
2.  Inspect SQLite and the journal before deciding idempotence:
    *   **SQLite revoked, journal present:** return the row, retry file
        deletion, and write no new mutation row.
    *   **SQLite not revoked, journal present:** skip the journal append,
        update the cache, run the SQLite/audit transaction, and delete files.
    *   **SQLite revoked, journal missing:** append the journal entry, update
        the cache, and retry file deletion. No new mutation row is written;
        this is a durability repair.
    *   **Neither revoked:** continue with the normal path below.
3.  If the row is missing and the journal lacks the serial, return
    `CERTIFICATE_NOT_FOUND`. If the journal has the serial but the row is
    missing, return `CERTIFICATE_NOT_FOUND` with detail
    `journal_orphan_revocation`; the operator can call
    `RevokeExternalCertificate` with the PEM to record metadata.
4.  Append `{serial, revoked_at, not_after, reason}` to the journal with
    `O_APPEND`, mode `0600`, fsync, and a directory fsync when the file is
    created. If the append fails, write a best-effort `failed` audit row and
    return an error; no certificate-registry, cache, or file change is made.
5.  Add the serial to the in-memory cache.
6.  In one SQLite transaction, set `revoked_at`/`revoked_reason` with
    `WHERE revoked_at IS NULL` and write the `certificate_revoke` audit row only
    when the update actually transitions the row. If the transaction fails, the
    journal and cache still enforce revocation; write a best-effort `failed`
    audit row and return an error. A retry follows the case table in step 2.
7.  Delete the certificate and key files. A deletion failure adds a
    `file_delete_failed` warning but does not roll back revocation.
8.  Return the updated row and warnings.

The journal is the durability source. SQLite is the queryable metadata source. Files are removed only after the serial is durably revoked.

##### 2.4.1 External certificate revocation

An administrator can revoke a certificate that is not in the registry, such as
a certificate issued before this feature. The
`pki.ExternalRevoker` interface and the `RevokeExternalCertificate` RPC accept
a PEM certificate and a reason:

1.  Parse the PEM and verify it with
    `x509.VerifyOptions{Roots: clientCAPool, KeyUsages:
    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}`. A malformed,
    expired, or untrusted PEM returns `CERTIFICATE_EXTERNAL_INVALID`.
2.  Extract `not_before`/`not_after`, the role and name from the URI SAN, and
    the serial. Normalize the serial with the same rule as §2.3 (lowercase hex,
    no leading zeros, 1–40 characters). Reject a missing/malformed SAN or
    serial with `CERTIFICATE_EXTERNAL_INVALID`.
3.  If a SQLite row exists for the serial, delegate to `Revoke` by serial
    before acquiring `revokeMu`, then run the orphan-file cleanup in step 7
    before returning. This is the normal orphan-with-row path and covers a row
    whose file fields are empty while legacy files remain on disk.
4.  For the no-row path, acquire `revokeMu` for steps 4–6. If the journal
    already contains the serial, skip the journal append and update the cache;
    continue with the row insert below.
5.  If the journal lacks the serial, append the journal entry with fsync and
    update the cache before writing SQLite. If the append or fsync fails, do not
    update the cache, insert the row, or delete files; write a best-effort
    failed audit row and return `JOURNAL_UNAVAILABLE`.
6.  In one SQLite transaction, insert the revocation-only row and the
    `certificate_revoke_external` audit row. The row stores
    `issued_by = "external"`, `issuer_serial = ""`, `request_id = NULL`,
    `certificate_pem`, `client_ca_pem`, `cert_file = ""`, `key_file = ""`,
    `not_before`/`not_after` from the parsed certificate, `created_at = now`,
    and `revoked_at`/`revoked_reason`.
7.  Look for the orphan files named `<role>-<name>-<serial>.crt/.key`. Delete
    any that exist. For the legacy `<role>-<name>.crt/.key`, parse the
    certificate and delete the pair only when its normalized serial matches;
    otherwise return a `file_delete_failed` mismatch warning and leave the
    files. A deletion failure returns a `file_delete_failed` warning.
8.  Return the row and warnings.

If the transaction in step 6 fails, the journal and cache still enforce
revocation. Write a best-effort `result=failed` audit row and return an error.
A retry finds the journal entry, skips the append, updates the cache, and
retries the row/audit transaction. If the step 6 audit insert itself fails, the
transaction fails, the cache still enforces, and the error is mapped to
`AUDIT_WRITE_FAILED` when the recorder wraps `audit.ErrWrite`.

The local UI exposes a "Revoke external certificate" form that accepts a PEM
only. The API function is `revokeExternalCertificate`, the response type is
`RevokeExternalCertificateResponse`, and the form is in
`CertificatesPanel.tsx`. It does not list unmanaged files as a table. Both the
UI form and the admin API support external revocation; the API is the
programmatic path.

#### 2.5 List, get, retention, and pagination

`List` returns unexpired certificate summaries, including revoked rows,
ordered by creation time descending and then by serial. It is paginated as
described in §2.1. `Get` returns one full row or `CERTIFICATE_NOT_FOUND`. `Get`
treats a row whose `not_after` is in the past as `CERTIFICATE_NOT_FOUND`.
Neither operation returns private key material.

A cleanup pass runs at startup and then hourly on `Service.Run`:

*   Delete SQLite rows whose `not_after` is in the past.
*   Remove the same serials from the revocation cache.
*   Delete their files if any remain.
*   Compact the revocation journal by atomically rewriting it with only
    entries whose `not_after` is in the future. Append and compaction share one
    journal mutex so an append cannot be lost. Automatic compaction is skipped
    while the journal is unhealthy; the repair path is manual.
*   Delete audit rows older than `max(audit_retention_days, max_ttl_days)` in
    batches of 100. The default retention is 365 days when `max_ttl_days` is
    365, so agent-issued certificates are covered. External or pre-feature
    certificates with `not_after` beyond that window may have their revocation
    audit rows pruned before expiry; the registry row and journal entry remain
    the enforcement record.
*   Update `kouja_certificate_registry_size`,
    `kouja_certificates_expired_total`, and
    `kouja_unmanaged_certificate_files`.

A revoked certificate whose original expiry has passed is safe to forget. TLS
verification rejects it by date even if the registry row is gone.

#### 2.6 Revocation journal and cache

The journal is a JSON-lines file at
`certificate_authority.revocation_log_file` (default
`/var/lib/kouja/revoked-serials.log`). Each line contains `serial`,
`revoked_at`, `not_after`, and `reason`. It is mode `0600`, appended with fsync,
and compacted atomically during cleanup under the same mutex as append.

**Append invariant.** Under the journal mutex, `Append` checks the last byte:

*   Empty file: write the new entry.
*   Last byte is `\n`: append the new entry.
*   File ends with a complete JSON object but no newline: append `\n` first,
    then the new entry.
*   Tail is not a complete JSON object: return `ErrJournalUnhealthy` and write
    nothing. A partial tail from a crash is never concatenated with a new
    entry.

A missing journal is treated as an empty journal and is created with mode
`0600` on first append. An unreadable journal (permissions or I/O error) is a
startup error. A malformed complete line or a partial trailing line enters
**degraded revocation mode**: the volume service continues, the loader skips
malformed lines, records their offsets, and loads later valid lines.
`kouja_revocation_journal_healthy` is 0. Appends are allowed only when the tail
is complete. Automatic compaction is skipped while unhealthy.

Repair is an offline operator procedure: stop the agent, back up the journal,
remove malformed or partial lines, ensure the file ends with `\n`, and restart.
On restart the loader recomputes health; the metric returns to 1 only when the
file parses completely. The design does not add an online repair RPC.
Revocations in malformed or partial lines are not recoverable. Re-revoke the
certificate with `RevokeCertificate(serial)` when a registry row exists; use
`RevokeExternalCertificate` only when the row is absent and the PEM is
available. The risk register records this gap.

**Backup and restore.** The journal is outside SQLite. A revocation is lost
only when both restored artifacts predate it: a newer database with an older
journal still enforces the database's revoked rows, and a newer journal with an
older database still enforces the journal entries. Restoring the journal at or
after the database is the safe ordering. If a revocation is absent from both
artifacts, re-revoke with `RevokeCertificate(serial)` when the registry row
exists; use `RevokeExternalCertificate` only when the row is absent and the PEM
is available.

`pki.Service` implements a `JournalSnapshotter`:

```text
JournalSnapshotter
  SnapshotJournal(ctx, dst string) error
```

`SnapshotJournal` holds the journal mutex, copies to a temporary file,
validates every complete JSON line, fsyncs, and renames. A missing journal
produces an empty snapshot. Any incomplete or malformed line returns
`ErrJournalUnhealthy`; the backup fails and the operator repairs the journal
and retries. The backup manager does not gain a `Restore` method.

The source path is `certificate_authority.revocation_log_file`.
`internal/backup.Manager` is extended with `JournalSnapshotter` and
`RevocationLogName` (destination basename, default `revoked-serials.log`). The
manager uses the same timestamp token as the database backup and calls
`SnapshotJournal(ctx, filepath.Join(TargetDir, timestamp+"-"+RevocationLogName))`;
the manifest field `revocation_log` stores the relative filename; `Rotate`
deletes `<timestamp>-<RevocationLogName>` with the database retention count. `cmd/kouja-agent`
passes the `pki.Service` as the snapshotter and the name as the target. Restore
is the documented operator procedure: copy the database, secret key, host ID,
and journal to their configured paths, then start the agent. The journal is an
additional target within the existing backup manager, not a separate backup
system or restore path.

`RevocationCache` is a `sync.RWMutex`-protected set of normalized lowercase
serial strings. It is the union of revoked, unexpired serials loaded from the
journal and SQLite at startup, and is updated inside `Revoke` after the journal
append. The journal is the durability source; SQLite is the metadata source.
`IsRevoked` is an O(1) map lookup. The cache is shared with `agent.Server`
through the narrow `RevocationChecker` interface.

#### 2.7 Failure modes

| Failure | Behavior |
|---|---|
| CA key missing, mismatched, or unsafe while enabled | Agent starts in degraded issuance mode; volume service continues; `Issue` returns `CERTIFICATE_CA_UNAVAILABLE`; metric is 0. |
| Certificate directory unsafe or wrong mode | Degraded issuance mode; list/revoke continue when the DB/journal is readable. |
| Issue request exceeds `max_active_certificates` | `ResourceExhausted`, reason `CERTIFICATE_QUOTA_EXCEEDED`. |
| File write fails during issue | Remove partial files, return an error, write no registry row, and write a best-effort failed audit row. |
| Registry/audit transaction fails after files are written | Remove the files, return an error; an audit insert failure returns `AUDIT_WRITE_FAILED` and writes the best-effort failed row. |
| Process crashes between file write and DB insert | Unmanaged file pair remains; it has no registry row. `RevokeExternalCertificate` can revoke it; the unmanaged metric increments. |
| Revoke journal append fails | No certificate-registry, cache, or file change; a best-effort failed audit row is written; return `JOURNAL_UNAVAILABLE`. |
| External revoke journal append fails | No cache, registry-row, or file change; a best-effort failed audit row is written; return `JOURNAL_UNAVAILABLE`. |
| Client CA file missing, invalid, or expired | The mTLS listener fails; this is not degraded issuance. |
| CA expires while running | `Issue` returns `CERTIFICATE_CA_UNAVAILABLE`, `Ready()` is false, `CAState()` is expired, and readiness is 0. |
| Revoke DB transaction fails after the journal append | The journal and cache enforce revocation; metadata is not updated; retry reconciles. |
| Revoke file deletion fails | Return success with a `file_delete_failed` warning. |
| Agent restarts | Reload journal and SQLite revoked serials. |
| Journal has a malformed complete line | Degraded revocation mode; volume service continues; `kouja_revocation_journal_healthy=0`; appends require a complete tail; automatic compaction is skipped; backup fails until repaired; operator repairs. |
| Journal has a partial trailing line | Append returns `ErrJournalUnhealthy`; automatic compaction is skipped; operator repairs and re-revokes the lost entry. |
| Journal is missing | Treated as empty; created with mode `0600` on first append. |
| Journal is unreadable (permissions or I/O) | Startup error. |
| Journal snapshot fails (unhealthy or I/O) | Backup fails; operator repairs and retries. |
| Existing pre-migration backup file | Always create a new timestamped backup; never skip or reuse an existing file; fail startup if it cannot be produced. |
| Older database is restored | A revocation is lost only when both artifacts predate it. Use `RevokeCertificate` when the row exists; `RevokeExternalCertificate` only when the row is absent and the PEM is available. |
| Expired certificate row | Cleanup removes the row, cache entry, files, and journal entry. |

### 3. State and Schema

The certificate registry and the audit extension are schema version 2. The
existing `schema_migrations` mechanism applies them once.

```text
certificates
  serial          TEXT PRIMARY KEY
  role            TEXT NOT NULL
  name            TEXT NOT NULL
  issued_by       TEXT NOT NULL
  issuer_serial   TEXT NOT NULL
  request_id      TEXT
  certificate_pem TEXT NOT NULL
  client_ca_pem   TEXT NOT NULL
  cert_file       TEXT NOT NULL DEFAULT ''
  key_file        TEXT NOT NULL DEFAULT ''
  not_before      TEXT NOT NULL
  not_after       TEXT NOT NULL
  created_at      TEXT NOT NULL
  revoked_at      TEXT
  revoked_reason  TEXT
```

`request_id` is nullable and non-unique because issuance is not idempotent and
because external/revocation-only rows may have no request. An empty
`cert_file`/`key_file` means the row has no files; external revocation rows use
empty strings, not NULL. `issuer_serial` is
the serial of the admin certificate that issued the certificate, or empty for
an external/UI-issued row. `client_ca_pem` stores the CA that signed the
certificate, so `GetCertificate` does not depend on the current CA file. Indexes: `certificates(not_after)` and `certificates(revoked_at)`. The
`request_id` index is not created because issuance is not idempotent.

The audit table gains explicit columns so certificate, class, and VG events do
not overload `volume_id` or free-form state:

```text
audit (existing columns plus)
  resource_type TEXT
  resource_id   TEXT
  role          TEXT
  name          TEXT
  detail        TEXT
```

`state.AuditEvent` fields are `CallerOwner`, `CallerSerial`, `OperationID`,
`RequestID`, `ResourceType`, `ResourceID`, `Role`, `Name`, `Action`, `Result`,
`ErrorCode`, `Detail`, and `RemoteAddr`. `PutAuditEvent` validates
`CallerOwner`, `Action`, and `Result` and generates a UUID `OperationID` when
the event does not supply one. The legacy `PutAudit` keeps its existing
validation and SQL; it does not delegate. The `internal/audit.Recorder` is the
only writer for new certificate, VG, and class mutations; existing `PutAudit`
callers in `internal/operations` remain unchanged. It writes through `state.Store.Transact` so a
certificate issue/revoke or a VG mutation can include the audit row in the same
transaction. Class mutations write the YAML first and then the audit row; an
audit failure there returns an `audit_write_failed` warning because the YAML
change is already durable.

The state package gains `Certificate` in `internal/state/types.go` and these
operations in `internal/state/certificates.go`:

*   `PutCertificate(Certificate) error` — insert; duplicate serial is an
    error; duplicate request ID is allowed.
*   `GetCertificate(serial string) (Certificate, error)` — returns
    `ErrNotFound` when missing or expired.
*   `ListCertificates(pageSize int32, pageToken string) ([]Certificate, string,
    error)` — unexpired rows, ordered; `pki` maps them to its own summary/page
    types.
*   `RevokeCertificate(serial, reason string, at time.Time) (Certificate,
    error)` — conditional update; already revoked returns the row.
*   `DeleteCertificate(serial string) error` — missing is a no-op.
*   `ListExpiredCertificates(before time.Time) ([]Certificate, error)`.
*   `ListRevokedSerials() ([]string, error)` — unexpired revoked serials.
*   `CountActiveCertificates(now time.Time) (int, error)` — counts
    `revoked_at IS NULL` and `not_after > now`; used by the quota.
*   `DeleteAuditBefore(before time.Time) (int64, error)` — deletes at most 100
    rows per call and returns the count. The pki cleanup loop repeats until the
    count is 0.

The certificate table is independent of the VG and volume tables. It does not
use foreign keys.

The revocation journal is a file, not a table. It is created with `0600`, is
appended with fsync, and is compacted atomically during cleanup. It is included
in the host backup alongside the SQLite database.

**Pre-migration backup:** `state.Open` must use the existing
`state.Store.Backup` (which already performs `VACUUM INTO`, fsync, mode `0600`,
and directory sync) before applying schema version 2. Use a timestamped name
such as `<data_dir>/state.db.pre-v2-<unix>.bak`, where `<unix>` is
`UnixNano`. Always create a new timestamped backup; if it cannot be created,
fail startup. In-memory stores skip the backup. Do not skip or reuse an
existing backup; the unique timestamp avoids `Store.Backup` returning
`ErrConflict` for an existing destination, and an unwritable data directory
fails startup. The backup is the only supported rollback
source.

**Rollback constraint:** an older agent binary refuses to open a schema
version 2 database. Rolling back requires restoring the pre-v2 backup. The
upgrade notes are `docs/upgrading.md`; they must state this, the config break,
the certificate file-name change, the external-revocation path, and the
CA-rotation fallback for unmanaged certificates without a usable PEM.

The design extends the existing `internal/backup.Manager` to copy the SQLite
database, secret key, host ID, and revocation journal. Certificate files are not in the database; the
CA key and certificate directory need their own host backup if the operator
wants to preserve issued keypairs.

### 4. mTLS Authentication and Revocation Enforcement

The current auth path has two entry points:

*   gRPC: the unary interceptor reads the peer certificate and calls
    `auth.Parse`.
*   HTTP/JSON: `withTLSIdentity` reads `request.TLS.PeerCertificates[0]` and
    calls `auth.Parse`.

Both must enforce revocation.

`auth.Identity` gains a `SerialNumber` field. `auth.Parse` fills it from the
peer certificate using the canonical lowercase-hex encoding. Existing tests
that build identities without a serial continue to work; an empty serial is
never revoked.

`agent.NewServer` gains `WithRevocationChecker(checker RevocationChecker)`.
`RevocationChecker` is a narrow interface:

```text
type RevocationChecker interface {
    IsRevoked(serial string) bool
}
```

`pki.Service` implements it. `NewServer` returns an error when no checker is
supplied; a missing wiring line must fail startup rather than silently
disabling revocation. The existing exported `UnaryInterceptor` is replaced by
a server method `server.unaryInterceptor` that closes over the checker. The
existing direct tests are updated to call the method or a test helper with a
fake checker.

The request flow becomes:

1.  The TLS handshake verifies the chain, validity dates, and client auth
    usage, as it does today.
2.  For a gRPC request, the unary interceptor resolves the identity, then
    checks `IsRevoked(identity.SerialNumber)`. A revoked serial returns
    `Unauthenticated` with reason `CERTIFICATE_REVOKED` and `retryable=false`.
3.  For an HTTP/JSON request, the outer handler parses the identity before
    dispatching to `/healthz`, `/readyz`, `/metrics`, or the gateway. A
    revoked serial returns HTTP 401 with a `google.rpc.Status` body carrying
    the same reason. The check happens before health and metrics.
4.  Existing TLS connections are checked per request, not only at handshake.

The gRPC path must not reject at the outer HTTP handler before the gRPC
framing is processed. The outer handler checks the `Content-Type`; gRPC
requests go to `grpcServer.ServeHTTP` and are rejected by the interceptor.
Non-gRPC requests are rejected before the gateway.

The local admin UI has no mTLS and no revocation check. Its loopback-only,
unauthenticated trust model is unchanged. The README must keep the
single-administrator precondition.

### 5. Admin API Changes

#### 5.1 Proto contract

The new RPCs and messages go in `proto/kouja/v1/admin.proto`. The capability
field goes in `GetInfoResponse` in `proto/kouja/v1/volume.proto`. The agent
always advertises `certificates` and `storage_classes`, including when
`certificate_authority.enabled` is false; disabled mode only changes `Issue`.

API contract sketch (illustrative, not production code):

```proto
message Warning {
  string code = 1;
  string message = 2;
}

message IssueCertificateRequest {
  string role = 1;
  string name = 2;
  int32 ttl_days = 3;       // 0 means the agent default
  string request_id = 4;    // audit correlation, not idempotency
}

message IssueCertificateResponse {
  string certificate_pem = 1;
  string private_key_pem = 2;
  string client_ca_pem = 3;
  string serial_number = 4;
  string role = 5;
  string name = 6;
  google.protobuf.Timestamp not_before = 7;
  google.protobuf.Timestamp not_after = 8;
  string request_id = 9;
  string issuer_serial = 10;
  repeated Warning warnings = 11;
}

message CertificateSummary {
  string serial_number = 1;
  string role = 2;
  string name = 3;
  string issued_by = 4;
  string issuer_serial = 5;
  google.protobuf.Timestamp not_before = 6;
  google.protobuf.Timestamp not_after = 7;
  google.protobuf.Timestamp created_at = 8;
  google.protobuf.Timestamp revoked_at = 9;
  bool revoked = 10;
}

message Certificate {
  string serial_number = 1;
  string role = 2;
  string name = 3;
  string issued_by = 4;
  google.protobuf.Timestamp not_before = 5;
  google.protobuf.Timestamp not_after = 6;
  google.protobuf.Timestamp created_at = 7;
  google.protobuf.Timestamp revoked_at = 8;
  string revoked_reason = 9;
  bool revoked = 10;
  string certificate_pem = 11;
  string client_ca_pem = 12;
  string issuer_serial = 13;
}

message ListCertificatesRequest {
  int32 page_size = 1;
  string page_token = 2;
}
message ListCertificatesResponse {
  repeated CertificateSummary certificates = 1;
  string next_page_token = 2;
}

message GetCertificateRequest { string serial_number = 1; }
message GetCertificateResponse { Certificate certificate = 1; }

message RevokeCertificateRequest {
  string serial_number = 1;
  string reason = 2;
  string request_id = 3;
}
message RevokeCertificateResponse {
  Certificate certificate = 1;
  repeated Warning warnings = 2;
  string request_id = 3;
}

message RevokeExternalCertificateRequest {
  string certificate_pem = 1;
  string reason = 2;
  string request_id = 3;
}
message RevokeExternalCertificateResponse {
  Certificate certificate = 1;
  repeated Warning warnings = 2;
  string request_id = 3;
}

message ClassMapping {
  string class_name = 1;
  string vg_name = 2;
}
message ListClassesRequest {}
message ListClassesResponse { repeated ClassMapping classes = 1; }
message GetClassRequest { string class_name = 1; }
message GetClassResponse { ClassMapping class = 1; }
message SetClassRequest {
  string class_name = 1;
  string vg_name = 2;
  string request_id = 3;
}
message SetClassResponse {
  ClassMapping class = 1;
  repeated Warning warnings = 2;
  string request_id = 3;
}
message RemoveClassRequest {
  string class_name = 1;
  string request_id = 2;
}
message RemoveClassResponse {
  repeated Warning warnings = 1;
  string request_id = 2;
}

message GetVGRequest { string vg_name = 1; }
message GetVGResponse {
  Capacity capacity = 1;
  string registered_vg_uuid = 2;
  bool uuid_drift = 3;
}
```

Service additions:

```proto
rpc IssueCertificate(IssueCertificateRequest) returns (IssueCertificateResponse) {
  option (google.api.http) = { post: "/v1/admin/certificates" body: "*" };
}
rpc ListCertificates(ListCertificatesRequest) returns (ListCertificatesResponse) {
  option (google.api.http) = { get: "/v1/admin/certificates" };
}
rpc GetCertificate(GetCertificateRequest) returns (GetCertificateResponse) {
  option (google.api.http) = { get: "/v1/admin/certificates/{serial_number}" };
}
rpc RevokeCertificate(RevokeCertificateRequest) returns (RevokeCertificateResponse) {
  option (google.api.http) = {
    post: "/v1/admin/certificates/{serial_number}:revoke"
    body: "*"
  };
}
rpc RevokeExternalCertificate(RevokeExternalCertificateRequest) returns (RevokeExternalCertificateResponse) {
  option (google.api.http) = {
    post: "/v1/admin/certificates:revoke-external"
    body: "*"
  };
}
rpc ListClasses(ListClassesRequest) returns (ListClassesResponse) {
  option (google.api.http) = { get: "/v1/admin/classes" };
}
rpc GetClass(GetClassRequest) returns (GetClassResponse) {
  option (google.api.http) = { get: "/v1/admin/classes/{class_name}" };
}
rpc SetClass(SetClassRequest) returns (SetClassResponse) {
  option (google.api.http) = {
    put: "/v1/admin/classes/{class_name}"
    body: "*"
  };
}
rpc RemoveClass(RemoveClassRequest) returns (RemoveClassResponse) {
  option (google.api.http) = { delete: "/v1/admin/classes/{class_name}" };
}
rpc GetVG(GetVGRequest) returns (GetVGResponse) {
  option (google.api.http) = { get: "/v1/admin/vgs/{vg_name}" };
}
```

`GetInfoResponse` gains:

```proto
repeated string capabilities = 7;
string caller_role = 8;
string caller_owner = 9;
```

The agent reports `certificates` and `storage_classes` when it supports the
new surfaces. The provider checks both at configure time and fails with a
typed diagnostic when either is missing.

The generated client and gateway are regenerated. The target list includes the
OpenAPI test updates and the generated HTTP method tests; the OpenAPI path keys
use the generator's camelCase names, including `{serialNumber}`,
`{className}`, and `{vgName}`.

#### 5.2 RPC behavior

| RPC | HTTP | Auth | Behavior |
|---|---|---|---|
| `IssueCertificate` | `POST /v1/admin/certificates` | admin | Always creates a new certificate. `request_id` is audit correlation only. Returns the keypair once. Not retry-safe. |
| `ListCertificates` | `GET /v1/admin/certificates` | admin | Paginated, unexpired summaries, including revoked rows. No private keys or PEMs. |
| `GetCertificate` | `GET /v1/admin/certificates/{serial_number}` | admin | Returns one row or `CERTIFICATE_NOT_FOUND`; expired rows are not found. |
| `RevokeCertificate` | `POST /v1/admin/certificates/{serial_number}:revoke` | admin | Appends the journal, updates the cache, updates SQLite and audit, then deletes files. Idempotent; retries reconcile journal-ahead (row exists, not revoked) or SQLite-revoked + journal-missing state. A journal-orphan (no row) returns `CERTIFICATE_NOT_FOUND` and requires `RevokeExternalCertificate` with a PEM. |
| `RevokeExternalCertificate` | `POST /v1/admin/certificates:revoke-external` | admin | Parses and verifies an external certificate, appends the journal and updates the cache first, then records the revocation-only row and audit row. |
| `ListClasses` | `GET /v1/admin/classes` | admin | Returns the live class map. |
| `GetClass` | `GET /v1/admin/classes/{class_name}` | admin | Returns one mapping or `CLASS_NOT_FOUND`. |
| `SetClass` | `PUT /v1/admin/classes/{class_name}` | admin | Creates or updates a mapping under `vgClassGate`. Rejects an unregistered VG or a VG already mapped to another class. |
| `RemoveClass` | `DELETE /v1/admin/classes/{class_name}` | admin | Removes a mapping under `vgClassGate`. Missing class is `CLASS_NOT_FOUND`. |
| `GetVG` | `GET /v1/admin/vgs/{vg_name}` | admin | Returns the stored registration plus one capacity observation and `uuid_drift`. |

`IssueCertificate` is the only new mutation that returns secret material. The
gRPC and HTTP clients must not log request or response bodies. The HTTP gateway
sets `Cache-Control: no-store` on the issue path through an outer response
middleware or a custom `runtime.WithErrorHandler`; a
`WithForwardResponseOption` alone does not run on gRPC errors. The test asserts
the header on success and on error responses that may carry key material.

Proto annotations use snake_case path placeholders (`{serial_number}`,
`{class_name}`, `{vg_name}`); OpenAPI and the generated Go client expose the
camelCase names (`serialNumber`, `className`, `vgName`). `GetVG` is the
provider's read path. `GetInfo` returns `caller_role` and
`caller_owner`; the provider uses `caller_role` for the admin check. `GetVG`
returns the stored UUID separately
from the observed capacity UUID. If they differ, `uuid_drift=true`; the
provider emits a warning and keeps the registered UUID rather than silently
adopting the observed one.

#### 5.3 VG and class administration

`AdminService` gains `WithCertificateService(issuer pki.Issuer, registry
pki.Registry, external pki.ExternalRevoker, files pki.FileStore)` and
`WithVGClassManager(*agent.VGClassManager)`; `WithVGClassGate` is removed.
`cmd/kouja-agent` passes the `pki.Service` and the manager to it. The local UI
receives the same services through the narrow interfaces and
`adminui.Options` gains `VGClass *agent.VGClassManager`,
`Audit *audit.Recorder`, and `CAStatus pki.CAStatus`.

A shared `agent.VGClassManager` owns the VG/class policy and is the sole owner
of `vgClassGate`. It is used by both the mTLS `AdminService` and the local admin
UI handlers. It wraps `VGService`, `ClassRegistry`, `vgClassGate`, and the
shared `audit.Recorder`. `AdminService.DeregisterVG` and the UI adapters must
not take the gate before delegating; the manager locks it once.

```text
agent.NewVGClassManager(vgs VGService, classes ClassRegistry,
                        gate *sync.Mutex, audit *audit.Recorder) *VGClassManager

VGClassManager
  RegisterVG(ctx, name, expectedUUID string, replace bool, ev state.AuditEvent) (VGRegistration, []warnings.Warning, error)
  DeregisterVG(ctx, name string, ev state.AuditEvent) ([]warnings.Warning, error)
  GetVG(ctx, name string) (VGRegistration, error)
  SetClass(ctx, name, vg string, ev state.AuditEvent) ([]warnings.Warning, error)
  RemoveClass(ctx, name string, ev state.AuditEvent) ([]warnings.Warning, error)
  ListClasses(ctx) ([]ClassMapping, error)
  GetClass(ctx, name string) (ClassMapping, error)

VGRegistration
  Name, RegisteredVGUUID string
  Capacity                lvm.Capacity
  UUIDDrift               bool
  StorageClass            string
  ObservedAt              time.Time

VGService (extended)
  RegisterWithAudit(ctx, name, uuid string, replace bool, ev state.AuditEvent) error
  DeregisterWithAudit(ctx, name string, ev state.AuditEvent) error
```

`RegisterWithAudit`/`DeregisterWithAudit` write the VG row and audit row in the
same `VGManager` transaction. After registration, `VGClassManager.RegisterVG`
calls its own `GetVG` once to fill `Capacity`, `UUIDDrift`, `StorageClass`, and
`ObservedAt`; if observation fails, it returns the stored row with
`Capacity.VGState = unavailable`. The existing `Register`/`Deregister` methods
stay as wrappers for current callers. `AdminService.RegisterVG` and `GetVG` map
`VGRegistration` to the proto responses. `NewAdminService` requires
`WithVGClassManager`; a nil manager is a startup error.

*   VG register/deregister writes the VG row and the audit row in the existing
    `VGManager` transaction. `VGManager` is extended to accept an audit event.
*   `GetVG` returns the stored registration plus one observation. If LVM
    observation fails, it returns the stored row with a non-nil `Capacity`
    whose `vg_state` is `unavailable`, `vg_uuid` is empty, sizes are zero, and
    `storage_class` is the stored mapping; `registered_vg_uuid` is the stored
    UUID; `uuid_drift` is `false`. It returns `VG_NOT_FOUND` only when no row is
    stored. The provider maps these fields and does not emit a drift warning
    when observation is unavailable.
*   `SetClass` and `RemoveClass` lock `vgClassGate`, check that the VG is
    registered, call `ClassRegistry.Set`/`Remove`, then write the audit row.
    An audit failure after the YAML write returns an `audit_write_failed`
    warning.
*   `SetClass` returns success without rewriting the YAML when the mapping is
    unchanged. The UI and mTLS paths share that behavior.
*   `RegisterVG` uses `replace=false`. A request-supplied UUID mismatch is
    `VG_UUID_MISMATCH`/`FailedPrecondition`; a stored-vs-observed mismatch with
    `replace=false` is `VG_ALREADY_EXISTS`/`AlreadyExists`.

The local UI's `vg.go` handlers become thin adapters over `VGClassManager`.
The admin service's VG/class RPCs delegate to the same manager. This removes
the policy duplication that exists on `main`.

#### 5.4 Error reasons

Add typed errors in `internal/pki` and map them in helpers similar to
`vgStatusError`. Every reason is attached as `google.rpc.ErrorInfo` with domain
`kouja.v1`.

| Condition | gRPC code | Reason |
|---|---|---|
| Issuance disabled | `FailedPrecondition` | `CERTIFICATE_ISSUANCE_DISABLED` |
| CA unavailable/degraded | `FailedPrecondition` | `CERTIFICATE_CA_UNAVAILABLE` |
| Invalid role | `InvalidArgument` | `CERTIFICATE_ROLE_INVALID` |
| Invalid name | `InvalidArgument` | `CERTIFICATE_NAME_INVALID` |
| Invalid TTL | `InvalidArgument` | `CERTIFICATE_TTL_INVALID` |
| Invalid certificate serial | `InvalidArgument` | `CERTIFICATE_SERIAL_INVALID` |
| Invalid page token | `InvalidArgument` | `CERTIFICATE_PAGE_TOKEN_INVALID` |
| Journal unhealthy or partial tail | `FailedPrecondition` | `JOURNAL_UNAVAILABLE` |
| Active certificate quota exceeded | `ResourceExhausted` | `CERTIFICATE_QUOTA_EXCEEDED` |
| Missing or expired certificate | `NotFound` | `CERTIFICATE_NOT_FOUND` |
| External certificate is not signed by the client CA or has no valid SAN | `InvalidArgument` | `CERTIFICATE_EXTERNAL_INVALID` |
| Revoked peer certificate | `Unauthenticated` | `CERTIFICATE_REVOKED` |
| File failure | `Internal` | `CERTIFICATE_FILE_ERROR` |
| Audit insert aborted the mutation | `Internal` | `AUDIT_WRITE_FAILED` |
| Class not found | `NotFound` | `CLASS_NOT_FOUND` |
| Class conflict | `AlreadyExists` | `CLASS_CONFLICT` |
| Invalid class or VG name | `InvalidArgument` | `CLASS_NAME_INVALID` |
| VG not registered | `FailedPrecondition` | `CLASS_VG_NOT_REGISTERED` |
| Class persistence lock busy | `Aborted` | `CLASS_PERSISTENCE_LOCKED` |
| Class config path unsafe | `FailedPrecondition` | `CLASS_CONFIG_UNSAFE` |
| CA key or certificate path unsafe | `FailedPrecondition` | `CERTIFICATE_CONFIG_UNSAFE` |
| VG request UUID mismatch | `FailedPrecondition` | `VG_UUID_MISMATCH` |
| VG stored/observed UUID conflict | `AlreadyExists` | `VG_ALREADY_EXISTS` |
| VG in use | `FailedPrecondition` | `VG_IN_USE` |
| VG referenced by a class | `FailedPrecondition` | `VG_CLASS_MAPPED` |
| VG not found | `NotFound` | `VG_NOT_FOUND` |
| VG observation unavailable during Register/List | `FailedPrecondition` | `VG_UNAVAILABLE` |

`docs/client-errors.md` gains the reason table and retry guidance.
`IssueCertificate` is not retried. `RevokeCertificate`,
`RevokeExternalCertificate`, `SetClass`, `RemoveClass`, `RegisterVG`, and
`DeregisterVG` are safe to retry with the same inputs. Requests rejected with
`CERTIFICATE_REVOKED` are not retryable.

#### 5.5 Audit

The shared `internal/audit.Recorder` writes at most one mutation audit row per
logical mutation. A successful certificate issue/revoke, VG register/deregister,
or class set/remove writes one row; a no-op writes zero; a best-effort failed
row is not a mutation row.

*   Certificate issue/revoke: the row is in the same SQLite transaction as the
    registry change.
*   VG register/deregister: the row is in the `VGManager` transaction.
*   Class set/remove: the row is written after the YAML write; an audit failure
    returns an `audit_write_failed` warning.
*   Repeated revoke: no new mutation row. The first transition to revoked is
    the mutation.
*   No-op `SetClass`: no mutation row; the command is a successful no-op.
*   Failed post-authorization mutations: write a best-effort `result=failed`
    row in a separate transaction after the failure. If that write also fails,
    log it and increment `kouja_audit_failure_total`.
*   Pre-validation failures (invalid role/name/TTL/serial, malformed page
    token, disabled issuance) write no row. A malformed external PEM is a
    post-auth verification failure and writes a best-effort failed row.

| Operation | Success | Post-auth failure | Audit insert failure |
|---|---|---|---|
| Issue | one mutation row | best-effort failed row for quota, CA-unavailable, or file-write failure; no row for disabled issuance or invalid input | transaction fails; `AUDIT_WRITE_FAILED` |
| Revoke — journal append/fsync failure | no mutation row | best-effort failed row; no journal/cache change; certificate remains valid | n/a |
| Revoke — SQLite/audit failure after journal append | one mutation row after retry | best-effort failed row; journal/cache enforce | transaction fails; `AUDIT_WRITE_FAILED`; journal/cache still enforce |
| External revoke | one mutation row | best-effort failed row for invalid PEM/SAN/serial, journal-append failure, or row-insert failure | transaction fails; `AUDIT_WRITE_FAILED`; journal/cache still enforce |
| VG register/deregister | one mutation row | best-effort failed row for `VG_ALREADY_EXISTS`/`VG_IN_USE`/`VG_CLASS_MAPPED`/`VG_UUID_MISMATCH`/`VG_NOT_FOUND` | transaction fails; `AUDIT_WRITE_FAILED` |
| Class set/remove | one mutation row | best-effort failed row for `CLASS_NOT_FOUND`, `CLASS_VG_NOT_REGISTERED`, `CLASS_CONFLICT`, or YAML/persistence failure | YAML is durable; return `audit_write_failed` warning and metric |
| No-op SetClass | zero rows | n/a | n/a |

`audit.ErrWrite` is the sentinel for an audit-insert failure. `RecordTx` wraps
insert errors with `fmt.Errorf("%w: %v", ErrWrite, err)`; the admin service
maps `errors.Is(err, audit.ErrWrite)` to `AUDIT_WRITE_FAILED`.
`kouja_audit_failure_total` increments on every audit write failure, including
the best-effort row and the class warning path.

Audit rows record caller identity, operation ID, request ID, resource type,
resource ID, role, name, action, result, error code, detail, and remote
address. The field mapping is fixed:

| Action | resource_type | resource_id | role | name | detail |
|---|---|---|---|---|---|
| certificate_issue | `certificate` | serial | cert role | cert name | `request_id`, `issuer_serial`, `ttl_days` |
| certificate_revoke | `certificate` | serial | cert role | cert name | revoke reason |
| certificate_revoke_external | `certificate` | serial | cert role | cert name | `external`, revoke reason |
| volume_group_register | `volume_group` | VG name | empty | VG name | `replace=false` |
| volume_group_deregister | `volume_group` | VG name | empty | VG name | empty |
| storage_class_set | `storage_class` | class name | empty | class name | VG name |
| storage_class_remove | `storage_class` | class name | empty | class name | empty |

`Caller.Owner` is the caller URI for mTLS and `local-admin` for the loopback UI.
`Caller.SerialNumber` is the caller certificate serial, or empty for the UI.
Certificate audit rows include the serial, role, and name. They never include
the private key or certificate PEM. Registry rows do store `certificate_pem`
and `client_ca_pem`; those columns are not copied to audit rows. The existing UI certificate `writeAudit` call
is removed; the UI passes the shared recorder instead.

### 6. Local Admin UI

The UI changes are limited to G1/G5/G6: list issued certificates, download the
certificate and private key, create a certificate, revoke a registry
certificate, and revoke an external certificate by PEM. The panel does not add
a separate unmanaged-files table, a TTL editor, or a new status-badge
component. The existing table and download links are kept. The list handler
loops `Registry.List` until the page token is empty, so the UI shows the full
registry rather than one 50-row page.

Routes:

*   `GET /api/certificates` returns all unexpired registry rows (the handler
    aggregates `Registry.List` pages) joined with `FileStore` file metadata by
    serial, and the CA file.
*   `POST /api/certificates` accepts `role`, `name`, and optional `ttl_days`.
*   `DELETE /api/certificates/{serial}` revokes a registry row.
*   `POST /api/certificates:revoke-external` accepts `{certificate_pem, reason}`
    and revokes an external/orphan certificate.
*   `GET /api/certificates/{file}` downloads a managed or unmanaged file.
*   `GET /api/ca` downloads the public client CA.

JSON shapes:

```text
CertificateFile
  name, serial_number, kind, role       string
  size_bytes                            int64
  modified_at, not_after, revoked_at    string
  download_url, key_download_url        string
  revoked                               bool

CertificatesResponse
  directory, error                      string
  issuance_enabled                      bool
  default_ttl_days, max_ttl_days        int
  certificates                          []CertificateFile
  ca                                    *CertificateFile
```

`issuance_enabled` is `config.Enabled && CAStatus.Ready()`; `error` is
`CAStatus.CAState()` when issuance is enabled. Disabled issuance still allows
list, download, and revoke.

```text
CreateCertificateResponse
  certificate, private_key              CertificateFile
  serial_number, role, name             string
  not_after                             string
  warnings                              []Warning

RevokeCertificateResponse
  certificate                           CertificateFile
  warnings                              []Warning

RevokeExternalCertificateResponse
  certificate                           CertificateFile
  warnings                              []Warning
```

HTTP mapping:

| Error | Status |
|---|---|
| `ErrDisabled` | 503 |
| `ErrCAUnavailable`, `ErrJournalUnhealthy` | 503 |
| `ErrInvalidRole`, `ErrInvalidName`, `ErrInvalidTTL`, `ErrExternalInvalid`, `ErrInvalidSerial` | 400 |
| `ErrNotFound` | 404 |
| `ErrQuotaExceeded` | 429 |
| `ErrFile`, `ErrUnsafeDirectory` | 500 |

Warnings map to `Warning{Code, Message}`:
`durability_uncertain`, `audit_write_failed`, and `file_delete_failed`. The create response carries warnings because a file-sync
failure may be a success-with-warning.
Revoked rows are not downloadable; the list marks them revoked and disables
the revoke action. The panel does not present unmanaged files as revocable.

The UI uses the shared `agent.VGClassManager` for VG/class mutations and the
shared `internal/audit.Recorder` for audit. The existing certificate
`writeAudit` call is removed. The UI's configuration editor treats every
`certificate_authority` field as startup-bound and reports
`restart_required` with reason `certificate_authority_changed`.

The admin UI tests are rewritten: the old certificate tests that set
`AdminUI.ClientCertDir`/`ClientCAKeyFile` are removed; new tests cover list,
create, revoke, download, disabled issuance, CA unavailable, warning mapping,
and the restart reason.

### 7. Configuration and Deployment

Add a top-level section:

```yaml
certificate_authority:
  enabled: false
  key_file: /etc/kouja/tls/ca.key
  certificate_dir: /etc/kouja/tls/clients
  revocation_log_file: /var/lib/kouja/revoked-serials.log
  default_ttl_days: 30
  max_ttl_days: 365
  max_active_certificates: 1000
```

Rules:

*   `enabled` defaults to false. When false, no CA key is required; issuance
    returns `CERTIFICATE_ISSUANCE_DISABLED`; listing, revocation, and the
    journal/cache still work.
*   `key_file` defaults to `<directory of listen.tls.client_ca_file>/ca.key`.
    It must be absolute. Content and permission checks happen in `internal/pki`
    only when enabled.
*   `certificate_dir` defaults to `<directory of listen.tls.cert_file>/clients`.
    The pki service creates it with `0700` when enabled if it is missing, then
    validates mode, ownership, and the TLS-root exclusion. Config validates
    only the absolute path and the TLS-root exclusion.
*   `revocation_log_file` defaults to `/var/lib/kouja/revoked-serials.log`.
    Config validates the absolute path and excludes the certificate directory,
    `listen.tls.cert_file`, `listen.tls.key_file`,
    `listen.tls.client_ca_file`, and `certificate_authority.key_file`.
    `pki.New` rejects symlinks and validates mode/ownership even when issuance
    is disabled. It is copied by the existing backup manager, not a separate
    backup path.
*   `default_ttl_days` defaults to 30 and must be between 1 and 365.
*   `max_ttl_days` defaults to 365, must be between 1 and 365, and must be
    greater than or equal to `default_ttl_days`.
*   `max_active_certificates` defaults to 1000 and must be positive. It counts
    unexpired certificates that are not revoked. A value of 0 is rejected; use a
    large value to approximate no quota.
*   The CA certificate remains `listen.tls.client_ca_file`. The server
    certificate and key remain under `listen.tls`.

The TLS-root exclusion compares the certificate directory against the
directories containing `listen.tls.cert_file`, `listen.tls.key_file`,
`listen.tls.client_ca_file`, and `certificate_authority.key_file`. Equality
with any of those directories is a configuration error.

Remove `admin_ui.client_cert_dir` and `admin_ui.client_ca_key_file` from
`AdminUIConfig`, validation, the installer template, the init script, the
example config, and the documentation. The `admin_ui` section keeps only
`enabled` and `address`.

`deploy/install/install.sh` writes the section with `enabled: false` and does
not create the CA key. `deploy/kouja-init.sh` writes `enabled: true` only when
it actually generated the CA key and an admin certificate. If the operator
declined either, it writes `enabled: false` and prints the enablement step.

Update `deploy/config.example.yaml`, `deploy/install/install.sh`,
`deploy/kouja-init.sh`, `README.md`, `docs/designs/admin-ui.md`, and add
`docs/upgrading.md`.

### 8. Terraform Provider

#### 8.1 Location and build

The provider entrypoint is `cmd/terraform-provider-kouja/main.go`; the
implementation lives under `internal/terraformprovider`. The provider is part
of the existing kouja Go module and uses `gen/client`. The Terraform Plugin Framework is a
provider-only dependency and is not linked into the agent binary. A nested module is a future improvement; the current choice
avoids a second module and a second release path.

Build and version:

*   `build-provider`: `CGO_ENABLED=0 go build -trimpath
    -ldflags "-X main.version=$(VERSION)" -o bin/terraform-provider-kouja
    ./cmd/terraform-provider-kouja`.
*   `VERSION` defaults to `git describe --tags --always --dirty`.
*   `cmd/terraform-provider-kouja/main.go` defines `var version = "dev"` and
    passes it to `providerserver.Serve`.
*   Provider dependencies are pinned in `go.mod`; the implementation records
    the resolved versions at implementation time. The major lines are
    Terraform Plugin Framework v1, framework validators v0, and
    terraform-plugin-testing v1.

#### 8.2 Provider configuration

```hcl
provider "kouja" {
  endpoint          = "https://storage.example:9443"
  server_ca_file    = "/etc/kouja/terraform/server-ca.crt"
  client_cert_file  = "/etc/kouja/terraform/admin.crt"
  client_key_file   = "/etc/kouja/terraform/admin.key"
  server_name       = "storage.example"
  timeout           = "30s"
}
```

Attributes and environment variables:

| Attribute | Environment | Required | Rule |
|---|---|---|---|
| `endpoint` | `KOUJA_ENDPOINT` | yes | Absolute `https://host:port` URL; no path, query, or fragment. |
| `server_ca_pem` | `KOUJA_SERVER_CA_PEM` | one of the CA pair, or system roots | Inline PEM for the server CA. |
| `server_ca_file` | `KOUJA_SERVER_CA_FILE` | one of the CA pair, or system roots | File path for the server CA. |
| `client_cert_pem` | `KOUJA_CLIENT_CERT_PEM` | one of the cert pair | Inline admin client certificate. |
| `client_cert_file` | `KOUJA_CLIENT_CERT_FILE` | one of the cert pair | File path for the admin client certificate. |
| `client_key_pem` | `KOUJA_CLIENT_KEY_PEM` | one of the key pair | Inline admin client key; sensitive. |
| `client_key_file` | `KOUJA_CLIENT_KEY_FILE` | one of the key pair | File path for the admin client key; sensitive. |
| `server_name` | `KOUJA_SERVER_NAME` | no | TLS server name override; defaults to the endpoint host. |
| `timeout` | `KOUJA_TIMEOUT` | no | Request timeout; default 30s. |

Precedence: an explicit HCL value wins over the environment variable, which
wins over the default. For each pair, exactly one of the PEM and file forms may
be effective. If both are set after precedence, configuration fails with a
diagnostic. The CA pair may be empty, in which case system roots are used. The
client certificate and key are required and are parsed; the public keys must
match.

TLS setup reuses the generated client:

*   A CA file uses `client.LoadTLSConfig` and then sets
    `tlsConfig.ServerName` when configured.
*   Inline PEM builds a `tls.Config` with TLS 1.3, the server CA pool, the
    client certificate, and `ServerName`, passed through
    `client.WithTLSConfig`.
*   A missing CA uses a `tls.Config` with `RootCAs=nil` (system roots) and the
    client certificate; it does not call `LoadTLSConfig` with an empty path.
*   Mixed PEM/file pairs are assembled by a small `buildTLSConfig` that loads
    each part from its effective source.
*   `timeout` is a positive Go duration string; zero/negative values are
    rejected.
*   The timeout is applied with `context.WithTimeout`; the provider does not
    build a separate HTTP transport or set `ForceAttemptHTTP2`.

`Configure` calls `GetInfo`, checks that `capabilities` contains
`certificates` and `storage_classes`, and checks `caller_role == "admin"`. It uses
`GetInfo.caller_role` as the role probe, so configuration does not pay an O(V)
LVM cost. The provider parses its own client certificate and stores its
serial and `not_after`. It emits a Terraform warning when the bootstrap
certificate expires within 30 days or 10% of its total validity period,
whichever is larger.

Provider internals:

```text
providerData
  client     providerClient
  ownSerial  string
  ownNotAfter time.Time

providerClient interface
  GetInfo(ctx) (*pb.GetInfoResponse, error)
  GetVG(ctx, name) (*pb.GetVGResponse, error)
  RegisterVG(ctx, name, expectedUUID, requestID string) (*pb.RegisterVGResponse, error)
  DeregisterVG(ctx, name string, requestID string) error
  ListClasses(ctx) (*pb.ListClassesResponse, error)
  GetClass(ctx, name string) (*pb.ClassMapping, error)
  SetClass(ctx, name, vg string, requestID string) (*pb.SetClassResponse, error)
  RemoveClass(ctx, name string, requestID string) (*pb.RemoveClassResponse, error)
  IssueCertificate(ctx, role, name string, ttlDays int32, requestID string) (*pb.IssueCertificateResponse, error)
  GetCertificate(ctx, serial string) (*pb.GetCertificateResponse, error)
  RevokeCertificate(ctx, serial, reason string, requestID string) (*pb.RevokeCertificateResponse, error)
```

The resources use `req.ProviderData.(*providerData)`. A shared
`diagnosticFromError` helper maps `client.Error` to severity, summary, and
detail using the reason table. The client wrapper attaches the mutation `requestID` with
`client.WithRequestID(ctx, id)`. VG/class Create, Update, and Delete also
generate a UUID per operation and pass it through the same context. The
`RemoveClass` wrapper returns `*pb.RemoveClassResponse` so warnings become
Terraform warning diagnostics. Read calls retry on
`client.IsRetryable` up to three times with 200ms/400ms/800ms backoff and
jitter. Mutations other than `IssueCertificate` retry once when the operation
is idempotent. `IssueCertificate` is never retried. Agent warnings from
`IssueCertificate`, class, and revoke responses become Terraform warning
diagnostics.

#### 8.3 Resource schemas and CRUD

The three resources share the provider client, reason mapping, and warning
diagnostics described in §8.2. Each resource below adds only its schema and
CRUD specifics.

#### `kouja_volume_group`

*   `name` (string, required, forces replacement).
*   `registered_vg_uuid`, `observed_vg_uuid`, `uuid_drift` (computed).
*   `state`, `storage_class` (computed).
*   `total_bytes`, `free_bytes`, `allocatable_bytes`, `extent_size_bytes`,
    `max_volume_bytes` (number, computed).
*   `observed_at` (string, computed).

Create calls `RegisterVG` with the name, an empty expected UUID, and a request
ID. Read calls
`GetVG`; `NotFound` removes the resource from state. If observation is
unavailable, the provider reads `capacity.vg_state=unavailable`,
`observed_vg_uuid=""`, zero sizes, and `uuid_drift=false`, preserves the stored
`storage_class`, and emits no drift warning. If `uuid_drift` is true, the
provider emits a warning diagnostic and keeps `registered_vg_uuid`; it does not
adopt the observed UUID. Delete calls `DeregisterVG`; `NotFound` is
success, and `VG_IN_USE`/`VG_CLASS_MAPPED` are diagnostics. Import uses the VG
name. No in-place update.

#### `kouja_storage_class`

*   `name` (string, required, forces replacement).
*   `vg` (string, required).

Create and Update call `SetClass`; the agent's `VGClassManager` serializes the
call under its own `vgClassGate`. The provider never takes the mutex. Read calls
`GetClass`; `NotFound` removes the resource from state. Delete calls
`RemoveClass`; `NotFound` is success. Import uses the class name. Renaming a
class is a replacement; use destroy-before-create or a two-step apply because
one VG maps to one class.

#### `kouja_client_certificate`

*   `role` (string, required, forces replacement; one of `admin`, `caller`,
    `controller`, `node`).
*   `name` (string, required, forces replacement; validator
    `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).
*   `ttl_days` (number, optional, forces replacement). Null/omitted sends 0 so
    the configured agent default (`default_ttl_days`, 30 unless overridden)
    applies. A set value must be 1..365; the agent rejects values above
    `max_ttl_days` with `CERTIFICATE_TTL_INVALID`.
*   `serial_number`, `certificate_pem`, `private_key_pem` (sensitive),
    `client_ca_pem`, `issued_by`, `not_before`, `not_after`, `revoked`
    (computed).

Create calls `IssueCertificate` with a new request ID. It validates that
`certificate_pem`, `private_key_pem`, `client_ca_pem`, and `serial_number` are
non-empty; otherwise it returns an error. Read calls `GetCertificate`.
`NotFound`, expired, or `revoked=true` removes the resource from state. Read
sets `certificate_pem` and `client_ca_pem` from the response and copies
`private_key_pem` from prior state; if the private key is absent, Read removes
the resource so the next plan recreates it. Delete calls `RevokeCertificate`;
`NotFound` is success. The provider refuses to revoke its own serial. Import is
not supported.

`create_before_destroy` is the documented replacement pattern. The serial
suffix makes it safe. The provider's own admin certificate is a bootstrap
credential issued outside Terraform.

TTL precedence: the configured agent default (`default_ttl_days`, 30 unless
overridden) applies when Terraform omits `ttl_days`. A set Terraform value
overrides the agent default only for that certificate. The configured maximum
is `max_ttl_days`, 365 unless overridden. Negative values are invalid; `0` is
the wire sentinel for "use the default." The shared `internal/naming` package owns the certificate regex
`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and the VG/class regex
`^[A-Za-z0-9+_.-]{1,127}$`; the provider and agent use it instead of adding a
third copy.

#### 8.4 State and replacement

The private key is stored in Terraform state. The provider marks it sensitive
and never logs it. The state backend must be encrypted and access-controlled.
`role`, `name`, and `ttl_days` force replacement. VG `name` forces replacement.
Storage class `name` forces replacement; `vg` updates in place.

#### 8.5 Error handling and retries

The provider maps `client.Error` and `client.ErrorInfo` to diagnostics. Reads
may retry when `client.IsRetryable` is true. `RegisterVG`, `DeregisterVG`,
`SetClass`, `RemoveClass`, and `RevokeCertificate` are idempotent and may retry
once. `IssueCertificate` is never retried; a lost response leaves an orphan
that is visible through `ListCertificates` and can be revoked manually. The
request ID is for audit correlation and is not reused across apply attempts.

#### 8.6 Release and registry constraint

The provider is built and released from the kouja repository. Terraform
Registry requires a repository named `terraform-provider-kouja` at its root,
so this design does not publish to the public registry. Operators install the
binary through a filesystem mirror, a private registry, or `dev_overrides`
during development. A separate mirror repository is a future improvement.

#### 8.7 Acceptance and test strategy

The automated strategy is:

*   Ginkgo unit tests for provider schema, configuration precedence, PEM/file
    resolution, endpoint validation, client certificate/key match, capability
    failure, admin-role failure, self-serial parsing, and diagnostic mapping.
*   `terraform-plugin-testing` acceptance tests against an `httptest` TLS
    server that implements `GetInfo`, `ListVGs`, `GetVG`, the VG/class RPCs, and
    the certificate RPCs. The server requires a client certificate, accepts
    the admin test certificate, rejects a caller test certificate, and drives
    the provider through the plugin protocol.
*   A manual QEMU real-agent procedure in `docs/e2e.md`. The automated suite
    does not depend on a real agent; the manual procedure is the real-agent
    check.

The fake server and the routes it implements are listed in
`internal/terraformprovider/testutil_test.go`. The provider factory uses the
address `registry.terraform.io/voidstarit/kouja`.

### 9. Data Flows

Certificate issuance:

```text
Terraform apply
  └─ provider Create
       └─ POST /v1/admin/certificates {role,name,ttl_days,request_id}
            └─ TLS 1.3 + admin identity + revocation check
                 └─ AdminService.IssueCertificate
                      └─ pki.Service.Issue
                           ├─ validate role/name/TTL
                           ├─ enforce max_active_certificates
                           ├─ generate ECDSA key + serial
                           ├─ sign with client CA
                           ├─ write .crt/.key files with fsutil
                           └─ one transaction: certificates row + audit row
       └─ state stores certificate_pem, private_key_pem, client_ca_pem, serial

A lost issue response leaves an orphan. The provider does not retry issue;
the orphan is visible through ListCertificates and is revoked manually.
```

Certificate revocation:

```text
Terraform destroy
  └─ provider Delete
       └─ POST /v1/admin/certificates/{serial}:revoke
            └─ AdminService.RevokeCertificate
                 └─ pki.Service.Revoke
                      ├─ validate serial
                      ├─ append serial to revocation journal (fsync)
                      ├─ add serial to revocation cache
                      ├─ one transaction: revoked_at + audit row
                      └─ delete .crt/.key files (warning allowed)

Next request from the revoked certificate
  └─ TLS handshake succeeds
       └─ request identity parsed
            └─ revocation cache hit
                 └─ UNAUTHENTICATED / CERTIFICATE_REVOKED
```

Class mapping:

```text
Terraform apply / local UI
  └─ VGClassManager.SetClass
       ├─ lock vgClassGate
       ├─ VGService.List — target VG must be registered
       ├─ ClassRegistry.Set — one VG per class, no duplicates
       ├─ config.SetClass — atomic YAML update + .bak
       └─ audit.Recorder — audit_write_failed warning if the row fails
```

VG registration and read:

```text
Terraform apply
  └─ provider Create
       └─ POST /v1/admin/vgs {vg_name}
            └─ VGClassManager.RegisterVG
                 ├─ VGManager.Register(replace=false)
                 ├─ one transaction: vgs row + audit row
                 └─ reject request UUID mismatch / stored UUID conflict

Terraform refresh
  └─ provider Read
       └─ GET /v1/admin/vgs/{vg_name}
            ├─ stored registration UUID
            ├─ observed LVM UUID + capacity
            └─ uuid_drift=true when they differ; provider warns and keeps registered UUID
```

Local UI certificate action:

```text
Browser
  └─ GET/POST/DELETE /api/certificates
       └─ adminui handler
            └─ pki.Service (same registry, files, journal, cache)
```

Restore:

```text
Host restore (manual procedure)
  ├─ restore SQLite database (possibly older)
  ├─ restore revocation journal at or after the database snapshot
  └─ startup loads revoked serials from both sources into the cache
       └─ revocations up to the journal snapshot still reject
```

## Security, Backup, and Rollback

### Security

The security controls are defined in **Non-Functional Requirements → Security & Privacy** and **Risks & Mitigations**. This section covers backup and rollback only.

### Backup

*   The SQLite database, including the certificate and audit tables, is covered
    by the existing backup process.
*   The design extends `internal/backup.Manager` to snapshot the revocation
    journal with the database. Restore is the documented manual procedure and
    copies the journal at or after the database backup as the safe ordering. A
    revocation is lost only when both artifacts predate it. Re-revoke with
    `RevokeCertificate(serial)` when the registry row exists; use
    `RevokeExternalCertificate` only when the row is absent and the PEM is
    available.
*   The CA key and certificate directory need their own host backup if the
    operator wants to preserve issued keypairs. The provider can reissue a
    certificate, but it cannot recover a lost private key.
*   `state.Open` creates `state.db.pre-v2-<unix>.bak` before applying schema
    version 2. The rollback path restores that file. The backup name is
    timestamped, so concurrent starts do not collide.

### Rollback and Rollout

Release ordering and compatibility are recorded in **Non-Functional
Requirements → Rollout & Rollback**; this subsection is the canonical backup
and rollback procedure.

1.  Deploy the agent with `certificate_authority.enabled: false`.
2.  The first start of the new binary creates the pre-v2 database backup and
    applies schema version 2.
3.  Install the CA key, enable issuance, and restart the agent.
4.  Bootstrap a long-lived admin certificate outside Terraform.
5.  Configure the provider and apply VGs, classes, and certificates.
6.  Roll back the provider independently; issued certificates remain valid.
7.  Roll back the agent only by restoring the pre-v2 backup, the matching old
    config (the new `certificate_authority` section fails the old binary's
    strict parsing), and the matching binary. An older binary has no journal or
    revocation interceptor, so rollback after any revocation requires client
    CA rotation first; otherwise revoked certificates become valid again. Pure
    issuance without revocation does not require rotation. Document this
    precondition in `docs/upgrading.md`.

`docs/upgrading.md` records the schema backup, the config break, the
certificate file-name change, the TTL change, the `replace=false` VG behavior,
the external-revocation path, and the CA-rotation fallback for unmanaged
certificates without a usable PEM.

## Verification Plan

*   **Build:** `make build`, `make build-go`, `make proto`, and
    `make build-provider`.
*   **Unit tests:** `make test` covers `internal/audit`, `internal/pki`,
    `internal/state`, `internal/auth`, `internal/agent`,
    `internal/config`, `internal/adminui`, `gen/client`, and
    `internal/terraformprovider`.
*   **Coverage:** `make coverage-check` keeps internal coverage at or above
    80%.
*   **Lint:** `make lint` and `make lint-provider`.
*   **Protocol:** `buf lint`, `make proto`, and a diff check on `gen/`
    including `gen/client/openapi_test.go` and `gen/client/http_test.go`.
*   **Audit tests:** exactly one mutation row per certificate issue/revoke,
    VG register/deregister, and class set/remove; the shared recorder generates
    `OperationID`; the old UI certificate audit call is absent; failed
    post-authorization mutations write a `failed` row.
*   **Issuance tests:** non-idempotent second issue creates a new serial;
    `GetCertificate` and `ListCertificates` never return a private key;
    cross-caller list/get cannot retrieve key material; quota returns
    `CERTIFICATE_QUOTA_EXCEEDED`; negative TTL is invalid; `ttl_days=0` uses
    the agent default; `max_active_certificates` is enforced.
*   **Revocation tests:** journal append before DB/cache/files; missing journal
    append leaves the certificate valid; DB failure after journal still rejects;
    restart and older-database restore still reject; file deletion failure
    returns a warning; repeated revoke writes no second mutation row;
    the journal-ahead retry reconciles the SQLite row; journal compaction
    preserves unexpired entries and serializes with append; the backup manager
    includes the journal, and the documented manual restore procedure copies it
    at or after the database.
*   **CA tests:** missing/mismatched key starts the volume service in degraded
    issuance mode; `Issue` returns `CERTIFICATE_CA_UNAVAILABLE`; list/revoke
    still work; disabled mode requires no key; certificate directory creation
    and TLS-root exclusion; CA expiry while running is rejected at issue time;
    malformed journal enters degraded revocation mode and sets the health
    metric.
*   **GetVG tests:** stored vs observed UUID drift is reported; the provider
    warns and keeps the registered UUID; LVM observation failure returns
    `vg_state=unavailable` and keeps state; missing VG removes state; delete
    `NotFound` is success.
*   **mTLS integration:** issue an admin and caller certificate, use them,
    revoke one, get `CERTIFICATE_REVOKED` on the next request, restart, and
    repeat. The helper writes the CA key, issues an admin certificate, and
    uses a temporary certificate directory.
*   **Provider tests:** unit tests for schema/config/precedence/self-serial,
    request-ID propagation, system-roots TLS, mixed PEM/file pairs, and
    near-expiry warning; `terraform-plugin-testing` acceptance for
    create/read/update/delete, import for VG/class, certificate replacement,
    conflict diagnostics, warnings-to-diagnostics, expired/revoked read
    behavior, and the self-revoke guard.
*   **UI tests:** list/create/revoke/download, external revoke, disabled
    issuance, CA unavailable, quota 429, warning mapping, restart reason, and
    the removed old config fields.
*   **Manual QEMU:** follow `docs/e2e.md` to register a real VG, map a class,
    issue and revoke a certificate, and verify the rejection.
*   **Rollback:** create the pre-v2 backup, migrate, attempt an older binary,
    restore the backup, and confirm the older binary starts.

## Alternatives Considered

### Alternative A: Provider-only against the existing admin API

*   **Pros**: No agent or proto changes. The provider can register and
    deregister VGs today. The change is limited to the provider.
*   **Cons**: Certificate issuance, certificate deletion, and storage-class
    management stay on the loopback UI or in the YAML file. Terraform can manage only one of the three resources.
*   **Reason for Rejection**: It does not satisfy G3/G4/G6: Terraform cannot
    create client certificates or storage classes.

### Alternative B: Drive the loopback admin UI through SSH or a tunnel

*   **Pros**: Reuses the existing certificate and class endpoints. No new
    mTLS admin RPCs. The UI already knows how to write files and edit the
    class map.
*   **Cons**: Requires SSH or a jump host for every apply. The UI is
    unauthenticated and root-equivalent, so the tunnel exposes a root-equivalent
    local surface to the automation host. There is no stable API contract, no
    caller identity in audit rows, and no way to run from a CI runner that has
    only network access to the agent.
*   **Reason for Rejection**: The agent already has an authenticated remote
    API. Making automation depend on an unauthenticated local UI moves the
    trust boundary in the wrong direction.

### Alternative C: CSR signing with Terraform-owned private keys

*   **Pros**: The private key never leaves Terraform or the caller. The agent
    never handles private key material. The provider can rotate keys without
    the agent writing a key file. Revocation can still be added.
*   **Cons**: The provider or the user must generate the key and CSR, manage
    the key lifecycle, and pass the CSR through the API. The agent must
    validate the CSR and replace the SAN. The current admin UI's
    server-generated flow would diverge from the API flow unless both are
    built.
*   **Reason for Rejection**: The design uses server-generated keypairs. The
    API and local UI share one issuance path; G10 accepts the Terraform state
    exposure.

### Alternative D: Server-generated keypairs without revocation

*   **Pros**: Reuses the current admin UI issuance code with the smallest
    change. No certificate table, no revocation cache, no auth-path change.
    Delete removes files.
*   **Cons**: A downloaded certificate remains valid until expiry after the
    files are deleted. Delete does not invalidate the credential. Terraform
    destroy does not
    invalidate the credential. The design would have to document that destroy
    does not revoke.
*   **Reason for Rejection**: Delete must invalidate the credential. A delete
    that only removes files does not do that.

### Alternative E: CSR signing without revocation

*   **Pros**: Private keys stay in Terraform, and the agent remains stateless
    for certificate issuance. The implementation is smaller than a registry
    plus revocation cache.
*   **Cons**: Destroy forgets the certificate but does not invalidate it.
    Containment depends entirely on short TTLs and rotation. There is no read
    or list operation for Terraform refresh.
*   **Reason for Rejection**: It has the same destroy gap as Alternative D and
    still requires a new API.

### Alternative F: Terraform or an external CA signs certificates

*   **Pros**: Terraform could use the `tls` provider and a CA key supplied
    through a secret backend, or an external CA/ACME service. The agent would
    not need a signing API.
*   **Cons**: Putting the CA private key in Terraform state or CI gives the
    automation host the ability to mint any certificate. An external CA adds
    a service dependency, CSR plumbing, and a second trust configuration.
    Neither fits the single-host agent model.
*   **Reason for Rejection**: The agent already holds the client CA and is the
    holds the CA key. Keeping it on the agent avoids distributing it to
    every Terraform runner.

### Alternative G: Separate `terraform-provider-kouja` repository or module

*   **Pros**: Independent versioning and release. Public Terraform Registry
    publication is possible. Terraform plugin dependencies stay out of the
    agent module.
*   **Cons**: The provider needs the generated client and the proto types.
    A separate module either duplicates them or pins a kouja module version,
    which creates cross-repository release coupling. The repository also needs
    its own CI, release, and security process.
*   **Reason for Rejection**: The provider entrypoint is
    `cmd/terraform-provider-kouja` in the kouja repository. Public registry
    publication is a non-goal and can be added later through a mirror
    repository.

### Alternative H: Reuse the existing admin UI certificate configuration fields

*   **Pros**: No configuration break. The `admin_ui.client_cert_dir` and
    `admin_ui.client_ca_key_file` fields already exist on `main`.
*   **Cons**: The fields are named for the UI even when the UI is disabled.
    The CA key and TTL policy are not UI concerns. A migration alias would
    preserve two certificate-config paths.
*   **Reason for Rejection**: The certificate feature is not deployed in
    production. A top-level `certificate_authority` section is one source, and the
    configuration break is documented in `docs/upgrading.md`.

### Alternative I: File-only certificate tracking

*   **Pros**: No schema migration. The current UI already lists files. A file
    is enough for a human to download a certificate.
*   **Cons**: Files cannot reliably answer whether a certificate was issued by
    the agent, when it expires, which role it has, whether it was revoked, or
    which request created it. Idempotent retries have no key. A copied file
    that is deleted is still a valid credential.
*   **Reason for Rejection**: Revocation, listing, Terraform read, and retry
    safety require durable metadata. The registry is the authority; files are
    a delivery format.

### Alternative J: External CRL or OCSP revocation

*   **Pros**: Standard revocation distribution. Other services could consume
    the CRL. An OCSP responder gives per-certificate status.
*   **Cons**: The agent is the only verifier of its own client certificates.
    A CRL file needs distribution and reload; an OCSP responder is another
    service to run and secure. Both add operational dependencies for a
    single-host agent.
*   **Reason for Rejection**: A cache backed by the append-only journal and
    SQLite metadata gives the agent immediate local enforcement. External
    CRL/OCSP distribution remains a non-goal.

### Alternative K: Periodic SQLite reload for revocation

*   **Pros**: No shared cache between the authority and the server. A reload
    loop adds no write-path coupling. Every process sees the
    database eventually.
*   **Cons**: Revocation is delayed by the reload interval. Every reload reads
    the table. A just-revoked certificate can still authenticate during the
    window. The delay has to be documented and tested.
*   **Reason for Rejection**: kouja runs one process per physical host and
    SQLite is local. A synchronous cache update gives immediate enforcement
    without a distributed consistency problem.

### Alternative L: Raw class-to-VG map API

*   **Pros**: Mirrors the `Storage.Classes` map and the live registry. One
    read returns everything. One write can replace the whole map.
*   **Cons**: A whole-map write makes concurrent Terraform resources
    overwrite each other. Terraform resource identity is per class, so a
    whole-map resource applies to one mapping. Partial failures are harder to
    report.
*   **Reason for Rejection**: Per-mapping endpoints match Terraform resource
    identity and make read-after-write and conflict reporting deterministic.

### Alternative M: Use `ListStorageClasses` as the class read API

*   **Pros**: The volume service already exposes class names, VG names, state,
    and capacity. The provider does not need a new class read RPC.
*   **Cons**: It returns capacity-resolved classes for registered VGs, not the
    raw mapping. A class mapped to an unavailable VG may be missing or
    represented differently. It has no admin write path. The provider cannot
    distinguish an absent mapping from an unavailable VG reliably.
*   **Reason for Rejection**: Terraform needs a stable, raw mapping read that
    does not depend on LVM capacity observation.

### Alternative N: Include volumes, exports, and VG creation in the provider

*   **Pros**: One Terraform workflow for the entire storage host. VGs could be
    created from block devices and volumes could be declared alongside them.
*   **Cons**: Volume lifecycle pulls in drift, credentials, ownership,
    data-loss confirmation, and CSI overlap. VG creation makes Terraform
    responsible for destructive block-device operations that kouja
    deliberately leaves to the administrator. The provider would become a
    second storage control plane.
*   **Reason for Rejection**: The provider scope is limited to certificates,
    VG registration, and class mappings. Volume lifecycle stays with the CSI
    driver or direct API callers, and VG creation stays a host operation.

### Alternative O: Ephemeral or write-only private key mode

*   **Pros**: Private keys do not land in Terraform state. A short-lived
    credential is generated at apply time and discarded.
*   **Cons**: Ephemeral resources require newer Terraform versions and a more
    complex schema. The provider would still need to return the certificate
    for the resource. The design assigns state security to the operator.
*   **Reason for Rejection**: The design assigns state security to the
    operator. The sensitive-attribute model is the v1 contract.

### Alternative P: Manage the provider's own admin certificate in Terraform

*   **Pros**: One lifecycle for every certificate, including the one the
    provider uses. Rotation could be automated by Terraform.
*   **Cons**: A single apply can revoke the certificate the provider is using.
    The next operation fails, and recovery needs manual state surgery or a
    second admin certificate. Terraform cannot update a provider's own
    credential mid-apply.
*   **Reason for Rejection**: The bootstrap admin certificate stays outside
    Terraform. The provider also refuses to revoke its own serial.

### Alternative Q: Separate SQLite database for certificate records

*   **Pros**: No schema version bump for the main database. Rolling back the
    agent would not hit the schema-newer error. Certificate data could be
    backed up separately.
*   **Cons**: Two databases means two backup and restore paths. A certificate
    issue or revoke cannot share a transaction with the audit row in the main
    database. The state package already owns the schema and backup process.
*   **Reason for Rejection**: One database keeps transactions and backups in one
    path. The rollback cost is a documented restore from the pre-upgrade
    backup.

### Alternative R: Keep revoked certificate records forever

*   **Pros**: A permanent revocation history in the certificate table.
    Serial numbers never disappear, so audit queries need no join.
*   **Cons**: The table grows without bound. Expired serials no longer need a
    revocation entry because TLS rejects them by date. The audit table is the
    long-term record for agent-issued certificates and is pruned after
    `max(audit_retention_days, max_ttl_days)`. External certificates with a
    longer `not_after` may lose their revocation audit row before expiry; the
    registry row and journal remain the enforcement record.
*   **Reason for Rejection**: Rows are pruned after `not_after`; the audit
    trail remains for at least the agent-issued maximum lifetime.

--------------------------------------------------------------------------------

## Non-Functional Requirements

### Security & Privacy

*   Certificate issuance is disabled by default. Enabling it requires a valid
    client CA certificate and matching key. A `certificate_authority.key_file`
    or `certificate_dir` failure puts issuance in degraded mode and keeps the
    volume service running. A missing, invalid, or expired client CA is a
    startup error.
*   Every new admin RPC requires the `admin` role. Every successful issuance
    and revocation, and every failed post-authorization mutation defined in
    §5.5, writes an audit row through the shared recorder.
*   The CA private key is a host file with mode `0600` or stricter. It is
    never returned by the API, listed by the UI, or written to the database.
*   The issue API is the only API response that returns the issued private
    key. The key is written to a `0600` file on the agent, is never written to
    SQLite, and is marked sensitive in Terraform state. The loopback UI may
    download the `0600` key file under its root-equivalent trust model.
    `GetCertificate` and `ListCertificates` never return it.
*   Revocation is checked per request for every role and every endpoint,
    including health and metrics. The journal makes revocation survive a
    restart and an older-database restore.
*   The local admin UI remains loopback-only and unauthenticated. Its revoke
    action is root-equivalent. The single-administrator host precondition
    remains.
*   The design does not publish a CRL or run OCSP. Certificates issued outside
    the registry can be revoked with `RevokeExternalCertificate` when their
    PEM is available and signed by the current client CA. Certificates without
    a usable PEM, or signed by a rotated CA, require client CA rotation. The
    agent exposes `kouja_unmanaged_certificate_files`.
*   A compromised admin certificate is equivalent to a CA compromise. The
    design records `issued_by` for attribution, requires a long-lived
    bootstrap certificate, and requires CA rotation on suspected admin
    compromise. Cascading revocation is a future improvement.

### Performance & Latency

*   Certificate issuance is operator-initiated. It performs one ECDSA P-256
    key generation, one CA signature, two file writes, and one SQLite
    transaction with the audit row.
*   `ListCertificates` is paginated (default 50, maximum 100). `page_token` is
    an opaque base64 encoding of `created_at|serial`; a malformed token returns
    `CERTIFICATE_PAGE_TOKEN_INVALID`. Issuance is bounded by
    `max_active_certificates` (default 1000).
*   The revocation check is an in-memory map lookup on every request. It adds
    no database read to the volume or admin request path.
*   The cleanup pass runs at startup and hourly. It deletes expired rows and
    files in batches of 100, compacts the journal atomically, and prunes audit
    rows older than `max(audit_retention_days, max_ttl_days)`.
*   The volume data path, LVM operations, and target configuration are
    unchanged.

### Rollout & Rollback

The canonical backup and rollback procedure is **Security, Backup, and
Rollback**; this subsection records release ordering and compatibility.

*   Deploy the agent before the provider. The provider requires the
    `certificates` and `storage_classes` capabilities.
*   The agent ships with `certificate_authority.enabled: false`. Issuance is
    opt-in after the CA key is installed.
*   The first start of the new binary creates `state.db.pre-v2-<unix>.bak` before
    applying schema version 2. An older agent refuses to open a version 2
    database; rollback restores the backup.
*   The old `admin_ui.client_cert_dir` and `admin_ui.client_ca_key_file` fields
    are removed without an alias. Existing configuration files that use them
    fail strict parsing and must be updated.
*   Certificate files use serial-suffixed names. Existing `<role>-<name>` files
    can be revoked with `RevokeExternalCertificate` when their PEM is available
    and signed by the current client CA; otherwise client CA rotation is
    required.
*   The CA key and certificate directory need their own host backup. The
    revocation journal is copied by `internal/backup.Manager` with the database
    and recorded in the manifest; the SQLite file alone does not contain it.
*   The provider version tracks the kouja release. There is no public Terraform
    Registry publication; installation is through a release binary, private
    registry, or filesystem mirror.

### Compatibility

*   Existing volume, export, credential, and CSI clients are unaffected.
*   `RegisterVG` changes from `replace=true` to `replace=false`. A client that
    relied on rewriting a VG UUID now receives `VG_UUID_MISMATCH` or
    `VG_ALREADY_EXISTS`, depending on the case.
*   The local UI certificate TTL changes from 825 days to the configured default
    and maximum (30 and 365 unless overridden).
*   The certificate file naming changes from `<role>-<name>` to
    `<role>-<name>-<serial>`. The local UI shows only registry rows. Old files
    can be revoked with `RevokeExternalCertificate` when their PEM is available
    and signed by the current client CA; otherwise CA rotation is required.
*   The old `admin_ui` certificate fields are removed without a compatibility
    alias.

### Observability

*   Audit rows cover certificate issuance, revocation, class set/remove, and
    VG register/deregister. They include the caller, operation ID, request ID,
    resource type, resource ID, role, name, action, result, and error code.
*   New metrics:
    *   `kouja_certificates_issued_total` (counter)
    *   `kouja_certificates_revoked_total` (counter)
    *   `kouja_certificates_expired_total` (counter)
    *   `kouja_certificate_issuance_failures_total` (counter)
    *   `kouja_certificate_registry_size` (gauge)
    *   `kouja_certificate_authority_ready` (gauge)
    *   `kouja_unmanaged_certificate_files` (gauge)
    *   `kouja_revocation_journal_healthy` (gauge)
    *   `kouja_audit_failure_total` (counter)
*   Certificate issue and revoke logs include the serial, role, name, and
    result. They never include the private key or certificate PEM.

## Risks & Mitigations

| Risk | Impact | Mitigation | Reference |
|---|---|---|---|
| Private key is returned to the provider and stored in Terraform state. | A state leak exposes a usable client credential. | Return the key only in the issue response; mark it sensitive; require an encrypted state backend; state security is the operator's responsibility. | §2.3, §8.3, §Security, Backup, and Rollback |
| An admin certificate can mint new admin certificates. | A compromised admin is equivalent to a CA compromise. | Record `issued_by`; require a long-lived bootstrap certificate; audit issuance; rotate the client CA on suspected admin compromise; cascading revocation is a future improvement. | §2.3, §5.5, §Security, Backup, and Rollback |
| Revocation is lost if the database is restored from before the revoke. | A revoked certificate authenticates again. | Append revocation to a journal outside SQLite and load both sources at startup; include the journal in host backups. | §2.4, §2.6, §Security, Backup, and Rollback |
| The journal is malformed or partially written. | A real revocation may be lost while the volume service continues. | Degraded revocation mode: malformed lines are skipped and reported, valid lines load, partial tails block append, backups fail, and `kouja_revocation_journal_healthy=0` until repaired. | §2.6, §2.7, §Verification Plan |
| Invalid CA key or directory material prevents issuance. | Certificate automation stops working. | Start in degraded issuance mode for key/directory failures; keep the volume service running; expose `kouja_certificate_authority_ready=0`. A missing/invalid/expired client CA fails startup. | §2.2, §2.7, §Security, Backup, and Rollback |
| A lost issue response leaves an orphan certificate. | An extra credential remains valid until revoked. | Do not retry issue; list and revoke the orphan; audit the request ID; document the recovery step. | §2.3, §8.5 |
| Certificates issued before this feature or outside the registry. | Credentials without a usable PEM or signed by a rotated CA remain unrevocable. | Revoke with `RevokeExternalCertificate` when the PEM is available and signed by the current client CA; otherwise rotate the CA or reprovision. Expose `kouja_unmanaged_certificate_files`. | §2.4.1, §Security, Backup, and Rollback |
| Schema version 2 is not backward-compatible with older binaries. | A direct binary rollback fails. | Create `state.db.pre-v2-<unix>.bak` before migration; restore it for rollback; document the step in `docs/upgrading.md`. | §3, §Security, Backup, and Rollback |
| The `admin_ui` certificate config fields and TTL behavior change. | Existing host configuration does not parse. | Remove the fields without an alias; list the break in `docs/upgrading.md`; fail strict parsing rather than silently ignoring. | §7, §Security, Backup, and Rollback |
| Class and VG mutations race. | A class can map to an unregistered VG. | One `VGClassManager`; lock the shared `vgClassGate` for Set/Remove and VG register/deregister; concurrency test. | §5.3, §Verification Plan |
| Audit writes fail. | A mutation is not attributable. | Certificate and VG audit share the mutation transaction; class audit failure returns `audit_write_failed`; the policy is explicit. | §5.5, §Verification Plan |
| `ListCertificates` grows without bound. | Large responses or registry growth. | Pagination (default 50, maximum 100) and `max_active_certificates` quota. | §2.3, §5.2, §Non-Functional Requirements |

--------------------------------------------------------------------------------

## Additional Resources

*   `proto/kouja/v1/admin.proto` and `proto/kouja/v1/volume.proto`.
*   `internal/agent/admin_service.go`, `internal/agent/server.go`,
    `internal/agent/class_registry.go`, and `internal/agent/vg_service.go`.
*   `internal/adminui/certificate_issue.go`, `internal/adminui/certificates.go`,
    and `internal/adminui/config_file.go`.
*   `internal/config/config.go` and `internal/state/schema.go`.
*   `docs/designs/kouja.md` and `docs/designs/admin-ui.md`.
*   `docs/clients.md` and `docs/client-errors.md`.
*   `README.md`, `docs/e2e.md`, and `docs/upgrading.md` (planned).
*   Pull request #8, which merged the local admin UI certificate issuance and
    class registry into `main`.

## Future Improvements

*   Add a CSR signing mode so Terraform or another client owns
    the private key and the agent never handles it.
*   Publish the provider through a separate
    `terraform-provider-kouja` mirror repository so `terraform init` can use
    the public registry.
*   Add an ephemeral or write-only private key mode for newer
    Terraform versions.
*   Publish a CRL or run an OCSP responder for consumers outside
    the agent.
*   Add certificate key escrow or recovery for operators who
    cannot rely on Terraform state.
*   Import externally issued certificates through a CSR or
    adoption flow.
*   Add `kouja_info`, capacity, and volume/export resources if
    the provider scope expands.
*   Automate the QEMU full-stack Terraform test instead of the
    manual procedure in `docs/e2e.md`.
*   Add an optional TTL field and bulk revocation to the local
    admin UI.
*   Design client CA rotation and trust-bundle overlap.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

### Implementation Order

The plan is grouped into five workstreams and delivered in four phases. The
file list below is the execution checklist; the workstreams are the review-level
plan. Every target file is assigned to a phase; files 16 and 17 are intentionally split.

**Workstreams**

*   **W1 Contract and audit foundation:** proto, generated code, OpenAPI/client
    tests, state schema, audit package, warnings package.
*   **W2 Certificate authority and revocation:** pki service, journal, fsutil,
    auth identity, agent server, certificate admin RPCs.
*   **W3 Admin API, UI, config, and startup:** shared VG/class manager, admin
    service, admin UI, web assets, config, agent main.
*   **W4 Terraform provider:** entrypoint, provider, resources, tests,
    dependency pins, Makefile provider targets.
*   **W5 Verification, release, docs:** integration/e2e, CI, deploy, docs,
    upgrade notes.

**Phase map and green checkpoints**

| Phase | Contents | Checkpoint |
|---|---|---|
| 1 | proto/generated/OpenAPI/client tests; state schema, certificate registry, audit, warnings, naming; config **additive** fields and `certificate_authority_test.go` only (old `admin_ui` fields and their tests remain) | `make proto`; `go build ./...`; `go test ./gen/... ./internal/state/... ./internal/audit/... ./internal/warnings/... ./internal/naming/... ./internal/config/...` |
| 2 | pki/journal/external; fsutil; backup; auth; agent server; admin service and shared VGClassManager; admin UI/web; config removal and `config_test.go`/`adminui_test.go` updates; example config; agent main; deploy init | `go build ./...`; `make test`; `make build` |
| 3 | provider; provider tests; go.mod/go.sum; Makefile; CI | `go build ./cmd/terraform-provider-kouja`; `make test-provider`; `make provider-acceptance` in CI with Terraform |
| 4 | deploy docs, `docs/upgrading.md`, final verification | `make all`; `make lint`; `make coverage-check`; manual `docs/e2e.md` |

Phase 1 is additive only. Phase 2 removes the old `admin_ui` certificate fields
together with every UI consumer, the example config, and the agent main wiring,
so its `go build ./...` and `make test` checkpoints are green. Phase 3 adds the
provider. Phase 4 is documentation and release.

| Phase | File numbers | Notes |
|---|---|---|
| 1 | 1–15, 16 (additive), 17 (additive), 19, 20, 27 | Proto/generated/state/audit/warnings/naming; config new fields only. File 18 stays Phase 2. |
| 2 | 16 (removal), 17 (removal), 18, 21–26, 28–61, 63, 87–89 | pki/journal/external, fsutil, backup, auth, agent, admin UI/web, config removal, example config, install/init. File 62 is not a target. |
| 3 | 64–86 | Provider, provider tests, examples, go.mod/go.sum, Makefile, CI. |
| 4 | 90–98 | README/docs, `docs/upgrading.md`, design finalization. |

Files 16 and 17 intentionally span Phase 1 and Phase 2: Phase 1 adds the new
config surface, Phase 2 removes the old fields. Every other target file is in
exactly one phase. `gen/client/openapi_test.go` and `gen/client/http_test.go`
are Phase 1; `internal/lvm/backend.go` is Phase 2.

Addendum files are assigned explicitly:

| Phase | Addendum files |
|---|---|
| 1 | `internal/audit/audit.go` and tests, `internal/warnings/*`, `internal/naming/*`, `internal/state/credentials.go`, `internal/config/storage.go` |
| 2 | `internal/pki/journal.go` and test, `internal/pki/external.go` and test, `internal/pki/testutil_test.go`, `internal/backup/backup.go` and test, `internal/fsutil/atomic.go`, `internal/agent/admin_vg_class.go` and test, `internal/agent/vg_service.go`, `internal/agent/vg_class_test.go`, `internal/agent/storage_class.go`, `internal/adminui/vg.go`, `internal/adminui/audit.go`, `internal/adminui/assets/index.html` (covered by file 58) |
| 3 | `internal/terraformprovider/config.go` (file 66) |
| 4 | `docs/upgrading.md` (file 98) |

`internal/state/credentials.go` is Phase 1 because it implements the new `Tx`
audit methods the Phase 1 build must satisfy.

### Target Files to Create/Modify

#### 1. `proto/kouja/v1/admin.proto` (Modify)

*   **Type / Scope**: Protobuf API contract.
*   **Proposed Changes**: Add the certificate, class, `GetVG`,
    `RevokeExternalCertificate`, `Warning`, `CertificateSummary`, and
    pagination messages from the Technical Plan. Add the `google.api.http`
    annotations for every new RPC. The `GetInfo` caller fields belong in
    `volume.proto` (file 2), not this file. Add field comments for the
    role/name/TTL constraints, the sensitive `private_key_pem` response, audit
    correlation by `request_id` (issuance is not idempotent), the `:revoke` and
    `:revoke-external` custom methods, and the `{vg_name}` route.
*   **Rationale**: This is the single source for the gRPC service, HTTP/JSON
    gateway, typed client, and OpenAPI document. The provider and the local UI
    both depend on this contract.

#### 2. `proto/kouja/v1/volume.proto` (Modify)

*   **Type / Scope**: Protobuf API contract.
*   **Proposed Changes**: Add `repeated string capabilities = 7;`,
    `string caller_role = 8;`, and `string caller_owner = 9;` to `GetInfoResponse`.
    Document the values `certificates` and `storage_classes`.
*   **Rationale**: The provider needs a positive feature check before it
    configures. An older agent that lacks the new RPCs returns an empty list,
    which produces a clear upgrade error instead of an `Unimplemented` failure
    in the middle of an apply.

#### 3. `gen/kouja/v1/admin.pb.go` (Generated, Modify)

*   **Type / Scope**: Generated protobuf messages.
*   **Proposed Changes**: Regenerate with `make proto`. Add the generated
    structs, getters, and descriptors for the new messages.
*   **Rationale**: The agent service and client cannot compile against the new
    contract without the generated types.

#### 4. `gen/kouja/v1/admin_grpc.pb.go` (Generated, Modify)

*   **Type / Scope**: Generated gRPC client and server.
*   **Proposed Changes**: Regenerate. Add the new client methods and the
    server interface methods, including the `UnimplementedAdminServiceServer`
    stubs.
*   **Rationale**: The `AdminService` implementation depends on these methods. The provider
    uses the HTTP/JSON client only.

#### 5. `gen/kouja/v1/admin.pb.gw.go` (Generated, Modify)

*   **Type / Scope**: Generated HTTP/JSON gateway.
*   **Proposed Changes**: Regenerate. Add the `POST /v1/admin/certificates`,
    `GET /v1/admin/certificates`, `GET /v1/admin/certificates/{serial_number}`,
    `POST /v1/admin/certificates/{serial_number}:revoke`,
    `POST /v1/admin/certificates:revoke-external`,
    `GET /v1/admin/vgs/{vg_name}`,
    `GET /v1/admin/classes`, `GET /v1/admin/classes/{class_name}`,
    `PUT /v1/admin/classes/{class_name}`, and
    `DELETE /v1/admin/classes/{class_name}` routes.
*   **Rationale**: The provider uses HTTP/JSON through the generated client, so
    the gateway routes must exist on the agent.

#### 6. `gen/kouja/v1/volume.pb.go` (Generated, Modify)

*   **Type / Scope**: Generated protobuf messages.
*   **Proposed Changes**: Regenerate to add `Capabilities`, `CallerRole`, and
    `CallerOwner` to `GetInfoResponse`.
*   **Rationale**: The provider reads this field during `Configure`.

#### 7. `gen/client/client.gen.go` (Generated, Modify)

*   **Type / Scope**: Generated typed HTTP client.
*   **Proposed Changes**: Regenerate with `make proto`. Add
    `IssueCertificate`, `ListCertificates`, `GetCertificate`,
    `RevokeCertificate`, `RevokeExternalCertificate`, `GetVG`, `ListClasses`,
    `GetClass`, `SetClass`, and `RemoveClass` methods. The generator already
    handles `get`, `post`, `put`, and `delete`, including path and query
    parameters, so no generator source change is needed.
*   **Rationale**: The provider uses this package instead of duplicating HTTP
    request construction. Regeneration keeps the client aligned with the
    proto.

#### 8. `gen/openapi/openapi.swagger.json` (Generated, Modify)

*   **Type / Scope**: Generated OpenAPI document.
*   **Proposed Changes**: Regenerate. Add the new admin paths and schemas.
*   **Rationale**: The repository publishes this document for API consumers.
    It must match the gateway.

#### 9. `internal/state/schema.go` (Modify)

*   **Type / Scope**: SQLite schema.
*   **Proposed Changes**: Add migration version 2 with the `certificates` table,
    the audit columns, and indexes on `not_after` and `revoked_at`. Do not add
    a `request_id` index; issuance is not idempotent.
*   **Rationale**: The certificate registry is durable state. The migration
    mechanism already applies and records versions atomically.

#### 10. `internal/state/store.go` (Modify)

*   **Type / Scope**: State store and transaction interface.
*   **Proposed Changes**: Set `schemaVersion = 2`. Before applying migration 2,
    call `Store.Backup` with `state.db.pre-v2-<unix>.bak`; always create a new
    timestamped backup and fail startup if it cannot be produced. In-memory
    stores skip it. Add the certificate methods to the `Tx` interface:
    `PutCertificate`, `GetCertificate`, `ListCertificates` (paginated),
    `RevokeCertificate`, `DeleteCertificate`, `ListExpiredCertificates`,
    `ListRevokedSerials`, `CountActiveCertificates`, and
    `DeleteAuditBefore(before time.Time)`. Add `PutAuditEvent(ev
    state.AuditEvent)` for the new audit columns and keep the existing
    `PutAudit` signature and behavior unchanged. `PutAuditEvent` takes a
    `state.AuditEvent` (no context; the Tx already carries it), not
    `audit.Event`, to avoid an import cycle. `PutAuditEvent` and
    `DeleteAuditBefore` are implemented in `internal/state/credentials.go` in
    Phase 1. Update any test fake that implements `Tx` in the same Phase 1
    commit.
*   **Rationale**: The authority needs transactional access to the registry.
    Keeping the methods on the existing `Tx` interface preserves the store's
    transaction and locking model.

#### 11. `internal/state/types.go` (Modify)

*   **Type / Scope**: State types.
*   **Proposed Changes**: Add a `Certificate` struct with serial, role, name,
    `IssuedBy`, `RequestID *string`, certificate PEM, `ClientCAPEM`,
    `IssuerSerial`, certificate file, key file, `NotBefore`, `NotAfter`,
    `CreatedAt`, `RevokedAt`, and `RevokedReason`. Define `AuditEvent` with the
    fields listed in §3 and extend `Audit` with the explicit fields.
*   **Rationale**: A shared type keeps the store, authority, and tests aligned.

#### 12. `internal/state/certificates.go` (Create)

*   **Type / Scope**: State persistence.
*   **Proposed Changes**: Implement the certificate methods declared in the
    `Tx` interface. Use the existing time formatting helpers. Allow a nullable, non-unique `request_id`. Return `ErrNotFound` for
    missing or expired serials. `RevokeCertificate` returns the updated row and
    is a conditional update. `ListCertificates` is paginated and ordered.
    `CountActiveCertificates` supports the quota. `ListExpiredCertificates`
    selects rows whose `not_after` is before the supplied time.
    `ListRevokedSerials` returns revoked, unexpired serials for the startup
    cache.
*   **Rationale**: The authority should not contain SQL. Keeping persistence
    in `internal/state` matches the rest of the codebase and makes the
    registry testable with a real temporary database.

#### 13. `internal/state/certificates_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover insert, duplicate serial, non-unique request ID, get by serial,
    pagination/order, revoke idempotency, expired-row selection,
    revoked-serial selection, count active, delete, and `DeleteAuditBefore`
    cutoff behavior.
*   **Rationale**: The registry is the authority for revocation. Its
    constraints need direct tests before the higher layers depend on them.

#### 14. `internal/state/store_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: The tests compare against the `schemaVersion` constant; there is no literal
    1 to change. Add a version 1 fixture using `migrations[1]` and a test that
    confirms the pre-v2 backup is created before version 2 is applied without
    losing existing rows.
*   **Rationale**: The schema version is asserted in several tests. The
    migration test protects upgrades from existing hosts.

#### 15. `internal/state/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Register the new `certificates_test.go` test
    functions with `It` entries and `GinkgoTB()`.
*   **Rationale**: Ginkgo specs must be registered. Plain `TestXxx` functions
    do not need this file; the new specs are Ginkgo `It` entries.

#### 16. `internal/config/config.go` (Modify)

*   **Type / Scope**: Configuration model.
*   **Proposed Changes**: **Phase 1:** add the top-level
    `CertificateAuthority` field and `CertificateAuthorityConfig` with
    `enabled`, `key_file`, `certificate_dir`, `revocation_log_file`,
    `default_ttl_days`, `max_ttl_days`, and `max_active_certificates`; apply
    defaults, path checks, TTL bounds, and the TLS-root exclusion. Keep
    `ClientCertDir` and `ClientCAKeyFile` in `AdminUIConfig` during Phase 1 so
    the existing admin UI still builds. **Phase 2:** remove
    `ClientCertDir` and `ClientCAKeyFile`, update every `internal/adminui`
    consumer in the same commit, and reject the removed `admin_ui` fields
    through strict YAML parsing. The CA certificate/key content checks stay in
    `internal/pki` because they require reading and parsing the files.
*   **Rationale**: Configuration is the only place that decides whether
    issuance is enabled and where the CA key and issued files live. Centralizing
    it avoids two sources of truth between the UI and the API.

#### 17. `internal/config/config_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: **Phase 1:** add fixtures for the new section's
    defaults and validation boundaries. `config_test.go` has no old-field
    fixtures. **Phase 2:** remove the old `config_test.go` fixtures. File 18
    alone owns the `adminui_test.go` rejection assertions.
*   **Rationale**: The configuration break is intentional. A test prevents a
    silent fallback that would leave an operator believing the old path still
    works.

#### 18. `internal/config/adminui_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: **Phase 2:** remove assertions for `ClientCertDir`
    and `ClientCAKeyFile`. Keep the `admin_ui` enabled, address, and default
    tests; the listener rejection test stays in `internal/adminui`. Remove the
    invalid relative-path tests for the deleted fields.
*   **Rationale**: The deleted fields no longer belong to `AdminUIConfig`. Their
    validation tests move to the new certificate authority test file.

#### 19. `internal/config/certificate_authority_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover defaults, disabled mode, enabled mode without a key path, relative
    paths, a certificate directory equal to the TLS root, the revocation-log
    path, `max_active_certificates`, TTL bounds, and `default_ttl_days` greater
    than `max_ttl_days`.
*   **Rationale**: These checks catch configuration mistakes before startup.
    The CA file content, ownership, and permission checks are covered by the
    `internal/pki` authority tests.

#### 20. `internal/config/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Register the new certificate authority tests.
*   **Rationale**: Ginkgo specs must be registered. Plain `TestXxx` functions
    do not need this file; the new specs are Ginkgo `It` entries.

#### 21. `internal/pki/authority.go` (Create)

*   **Type / Scope**: Certificate authority service.
*   **Proposed Changes**: Define the narrow `Issuer`, `Registry`, `FileStore`,
    `ExternalRevoker`, `RevocationChecker`, and `CAStatus` interfaces and the
    concrete `Service`; define the `Caller`, `IssueRequest`,
    `IssuedCertificate`, `CertificateRecord`, `CertificatePage`, `ListOptions`,
    and `FileInfo` types. Implement `New(Options) (*Service, error)`,
    `Run(ctx) error`, `Registry.Get`, `Registry.List`,
    `CAStatus.Ready() bool`, and `CAStatus.CAState() error`. Map
    `state.Certificate` to `CertificateRecord`/`CertificateSummary`: copy the
    serial, role, name, issuer, `RequestID *string`, PEMs, files, and times;
    derive `Revoked` from `RevokedAt`; expired rows return
    `CERTIFICATE_NOT_FOUND` at `Get` and are omitted from `List`. The
    constructor
    loads and validates the client CA and signing key, enters degraded issuance
    mode only for `key_file`/`certificate_dir` failures, returns a startup
    error for a missing/invalid/expired client CA, loads the journal and SQLite
    revocation sets, creates the certificate directory with `0700` when
    enabled, and validates ownership, mode, and path safety. It receives the state store, audit recorder, logger, clock, and
    metrics.
*   **Rationale**: One component owns the CA key, file layout, registry, and
    cache. Both admin surfaces call it, which prevents the UI and API from
    drifting.

#### 22. `internal/pki/issue.go` (Create)

*   **Type / Scope**: Certificate issuance.
*   **Proposed Changes**: Implement the non-idempotent issue flow: role/name/TTL
    validation, quota check, request-ID generation for audit correlation, ECDSA
    P-256 key generation, canonical serial generation, X.509 template
    construction, CA signing, `fsutil.WriteFileExclusiveAtomic` writes, and one
    transaction for the registry row and audit row. Return the certificate PEM,
    private key PEM, client CA PEM, serial, role, name, validity window, and
    request ID. If the transaction fails, remove the files and return
    `AUDIT_WRITE_FAILED` when the failure is an audit insert; the separate
    best-effort failed row follows §5.5. A file-write failure removes partial
    files, writes the best-effort failed row, and returns an error. A second
    call always creates a new certificate.
*   **Rationale**: This is the only place that handles private key material
    during issuance. Keeping it isolated makes the security review and tests
    tractable.

#### 23. `internal/pki/revoke.go` (Create)

*   **Type / Scope**: Certificate revocation.
*   **Proposed Changes**: Implement the revoke flow: serial validation and
    normalization, registry lookup, idempotent already-revoked handling, append
    to the revocation journal with fsync, cache update, one transaction for the
    SQLite row and audit row, and file deletion. Return a
    `file_delete_failed` warning when deletion fails. A journal append failure
    writes the best-effort failed audit row and returns an error; do not touch
    SQLite, the cache, or files. A SQLite/audit failure after the journal append
    leaves the journal and cache enforcing; a retry follows the §2.4 case table,
    not an early return.
*   **Rationale**: The ordering is the security boundary. A revoked serial must
    be durably recorded before the files disappear.

#### 24. `internal/pki/files.go` (Create)

*   **Type / Scope**: Certificate file layout and safety.
*   **Proposed Changes**: Implement the `<role>-<name>-<serial>.crt` and
    `.key` naming, `fsutil.WriteFileExclusiveAtomic` writes with cleanup on
    failure, role parsing from file names and certificates, the sensitive
    name filter, path traversal protection, and unmanaged-file listing.
    Implement `FileStore.ListFiles(ctx) ([]FileInfo, error)` and
    `FileStore.ResolveFile(name string) (string, error)`. Move the role and
    sensitive-name helpers out of `internal/adminui`.
*   **Rationale**: File naming and path safety must be identical for the UI and
    the API. Duplicating them in two packages invites a traversal or overwrite
    bug.

#### 25. `internal/pki/cache.go` (Create)

*   **Type / Scope**: Revocation cache.
*   **Proposed Changes**: Implement the RWMutex-protected serial set with
    `Load`, `IsRevoked(serial string) bool`, `Add`, `Remove`, and a snapshot
    for tests. `IsRevoked` is the `RevocationChecker` implementation. `Load` reads revoked, unexpired serials from the journal and the store at
    startup. The cache is updated after the journal append.
*   **Rationale**: The auth path needs an O(1) check that is independent of
    SQLite per request. The cache is small and rebuilt from the database.

#### 26. `internal/pki/errors.go` (Create)

*   **Type / Scope**: Typed errors.
*   **Proposed Changes**: Define `ErrDisabled`, `ErrCAUnavailable`, `ErrInvalidRole`,
    `ErrInvalidName`, `ErrInvalidTTL`, `ErrQuotaExceeded`, `ErrExternalInvalid`,
    `ErrInvalidSerial`, `ErrJournalUnhealthy`, `ErrNotFound`, `ErrFile`, and
    `ErrUnsafeDirectory`. Add `IsNotFound`, `IsExternalInvalid`, and
    `IsJournalUnhealthy` helpers used
    by the admin service and the UI.
*   **Rationale**: The admin service and UI need stable error categories to map
    to gRPC codes and HTTP statuses without string matching.

#### 27. `internal/audit/audit.go` (Create)

*   **Type / Scope**: Shared audit recorder.
*   **Proposed Changes**: Define `Event`, `Recorder`,
    `NewRecorder(store *state.Store, logger *slog.Logger, metrics
    *observability.Metrics) *Recorder`, `Record(ctx, Event) error`, and
    `RecordTx(tx state.Tx, Event) error`.
    `Event` carries caller, operation ID, request ID, resource type, resource
    ID, role, name, action, result, error code, detail, and remote address.
    `RecordTx` calls `tx.PutAuditEvent(state.AuditEvent{...})`; `Record` opens
    its own transaction. `audit.ErrWrite` wraps audit-insert failures.
    Generate the operation ID when absent. Never include private keys or
    certificate PEMs. The pki service, admin service, and local UI all call
    this recorder.
*   **Rationale**: One writer prevents three audit implementations and the
    duplicate UI certificate row.

#### 28. `internal/pki/authority_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover CA loading, disabled mode, degraded issuance
    mode on missing/mismatched `certificate_authority.key_file` or
    `certificate_dir` failures, and a hard startup error for a missing,
    invalid, or expired client CA, non-CA certificate, expired CA, unsafe certificate directory, journal +
    SQLite cache load, quota, and cleanup of expired rows, files, and journal
    entries.
*   **Rationale**: Startup and cleanup failures are security-critical. They
    need direct tests with temporary directories and a real SQLite store.

#### 29. `internal/pki/issue_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover role/name/TTL validation, negative TTL, `0` default, quota,
    CA-expiry capping, canonical serial format including a leading-zero serial,
    SAN/key usage, file modes/names, a second issue creating a new serial,
    transaction failure cleanup, and audit content.
*   **Rationale**: These tests define the observable issuance contract that the
    provider and API rely on.

#### 30. `internal/pki/revoke_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover unknown serial, idempotent already-revoked
    serial, journal-first ordering, journal append failure (including the
    best-effort failed row), DB failure after journal, cache update, file
    deletion, file deletion warning, audit content, the three §2.4
    reconciliation cases, the journal-orphan case, a partial-tail append
    failure, and a concurrent-revoke case that appends one journal entry and
    writes one mutation audit row.
*   **Rationale**: Revocation is the operation that makes Terraform destroy
    meaningful. Its failure ordering must be tested, not assumed.

#### 31. `internal/pki/files_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover file naming, exclusive creation, role parsing,
    sensitive-name filtering, path traversal rejection, unmanaged-file
    listing, and missing-file errors.
*   **Rationale**: The certificate directory contains private keys. Path and
    name handling need direct tests.

#### 32. `internal/pki/cache_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover load, add, remove, missing serial, expiry, and
    concurrent reads.
*   **Rationale**: The auth path calls the cache on every request. It must be
    race-free and correct after cleanup.

#### 33. `internal/pki/ginkgo_suite_test.go` (Create)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Add the suite runner and register the test functions
    from the new test files.
*   **Rationale**: Ginkgo specs must be registered. Plain `TestXxx` functions
    do not need this file; the new specs are Ginkgo `It` entries.

#### 34. `internal/auth/types.go` (Modify)

*   **Type / Scope**: Authentication identity.
*   **Proposed Changes**: Add `SerialNumber string` to `Identity`. Fill it from `peer.SerialNumber` using the canonical lowercase-hex
    encoding in `Parse`.
*   **Rationale**: The revocation check needs the peer serial. Keeping it on
    the identity avoids reparsing the certificate in every interceptor.

#### 35. `internal/auth/types_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Assert the serial is copied for a certificate with a
    serial, is empty for a nil/zero serial, and the existing role and SAN
    behavior is unchanged.
*   **Rationale**: A missing or malformed serial would silently disable
    revocation checks.

#### 36. `internal/auth/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Register the new serial test.
*   **Rationale**: New Ginkgo specs must be registered.

#### 37. `internal/agent/admin_service.go` (Modify)

*   **Type / Scope**: Admin gRPC service.
*   **Proposed Changes**: Add the narrow `pki.Issuer`, `pki.Registry`,
    `pki.ExternalRevoker`, and `pki.FileStore` fields, the
    `WithCertificateService` option, the shared `agent.VGClassManager` via
    `WithVGClassManager`, and `GetVG`. Remove `WithVGClassGate` and the
    `vgClassGate` field. Change `RegisterVG` and `DeregisterVG` to delegate to
    the manager with the caller audit event; the manager is the sole gate
    owner. Keep the shared `vgs` and `classRegistry` wiring.
*   **Rationale**: The service is the single mTLS entry point for the new
    operations. The VG change makes Terraform registration non-destructive and
    brings the mTLS path's audit behavior in line with the local UI.

#### 38. `internal/agent/certificate_admin.go` (Create)

*   **Type / Scope**: Admin gRPC service methods.
*   **Proposed Changes**: Implement `IssueCertificate`, `ListCertificates`,
    `GetCertificate`, `RevokeCertificate`, and `RevokeExternalCertificate`.
    Require the `admin` role. Resolve the caller and request ID, call the
    authority or `ExternalRevoker`, map the certificate errors to the reasons
    in the Technical Plan, and return the proto responses. Do not write
    certificate audit rows here; the authority owns them.
*   **Rationale**: Separating the certificate RPCs from the large admin service
    file keeps the proto mapping and error handling reviewable.

#### 39. `internal/agent/class_admin.go` (Create)

*   **Type / Scope**: Admin gRPC service methods.
*   **Proposed Changes**: Delegate the class RPCs to the shared
    `agent.VGClassManager`. Require the `admin` role. The manager locks
    `vgClassGate` for `SetClass` and `RemoveClass`, validates the class/VG
    names, verifies the VG is registered, calls `ClassRegistry.Set`/`Remove`,
    returns the shared `warnings.Warning` values, and writes the audit row
    through the shared recorder. Policy failures
    (`CLASS_VG_NOT_REGISTERED`, `CLASS_CONFLICT`) write the best-effort
    `result=failed` audit row.
*   **Rationale**: The local UI and the mTLS API must use the same live registry
    and the same VG/class gate. A separate file keeps the class semantics
    together.

#### 40. `internal/agent/server.go` (Modify)

*   **Type / Scope**: mTLS server and authentication.
*   **Proposed Changes**: Add `WithRevocationChecker` and a revocation checker
    field. Create the gRPC server after applying options so the unary
    interceptor can close over the checker. Check the cache after resolving the
    identity in the gRPC interceptor. For non-gRPC requests, check before
    dispatching to health, metrics, or the gateway and return HTTP 401 with a
    `google.rpc.Status` body and `CERTIFICATE_REVOKED`. Do not reject gRPC
    requests before the gRPC framing is processed. Add an outer response header
    middleware that sets `Cache-Control: no-store` on
    `/v1/admin/certificates` issue responses, including error responses. Keep
    the existing TLS configuration unchanged.
*   **Rationale**: Revocation must cover both transports and all endpoints,
    including health and metrics, without breaking the gRPC framing.

#### 41. `internal/agent/errors.go` (Modify)

*   **Type / Scope**: Error mapping.
*   **Proposed Changes**: Add a reusable status-with-reason helper and a
    `revokedError` that returns `Unauthenticated`, reason
    `CERTIFICATE_REVOKED`, and `retryable=false`. Update `vgStatusError` in
    `internal/agent/admin_service.go` to attach `VG_ALREADY_EXISTS`,
    `VG_IN_USE`, `VG_CLASS_MAPPED`, `VG_UUID_MISMATCH`, `VG_NOT_FOUND`, and
    `VG_UNAVAILABLE` reasons. Add the certificate and class reason helpers.
*   **Rationale**: The client SDK and provider rely on stable `ErrorInfo`
    reasons. String-only status messages would force clients to parse text.

#### 42. `internal/agent/volume_service.go` (Modify)

*   **Type / Scope**: Volume service and `GetInfo`.
*   **Proposed Changes**: Add `Capabilities []string` to `ServiceOptions`;
    read the caller identity from the context and return `CallerRole` and
    `CallerOwner` from `GetInfo`. Do not change any volume behavior.
*   **Rationale**: The provider needs a positive feature check. `GetInfo` is
    already available to any authenticated client.

#### 43. `internal/agent/admin_service_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Pass `WithVGClassManager` in every `NewAdminService`
    test setup. Update the VG registration test for `replace=false`. Add both
    UUID-mismatch cases: request-supplied mismatch expects
    `FailedPrecondition`/`VG_UUID_MISMATCH`; stored-vs-observed mismatch expects
    `AlreadyExists`/`VG_ALREADY_EXISTS`. Add `GetVG` tests for stored/observed
    drift, `vg_state=unavailable` on LVM failure, and missing-row `VG_NOT_FOUND`.
    Assert the register response `Capacity` and the audit row for register and
    deregister. Keep the existing non-admin and volume-in-use
    coverage.
*   **Rationale**: The VG policy change is a behavior change. It needs a test
    that would fail if `replace=true` returned.

#### 44. `internal/agent/certificate_admin_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Use fake `pki.Issuer`, `pki.Registry`, and
    `pki.ExternalRevoker` implementations to cover non-admin rejection, issue
    success and error mapping, list/get, revoke success and warnings, external
    revoke success/invalid PEM/reconciliation, external journal-append failure,
    disabled issuance, invalid serial, and caller/request-ID propagation.
*   **Rationale**: The RPC layer's job is authorization, mapping, and error
    translation. The authority's own tests cover crypto and persistence.

#### 45. `internal/agent/class_admin_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Use a fake `VGService` and a fake or real
    `ClassRegistry` to cover non-admin rejection, list sorting, get, set with a
    registered VG, set with an unregistered VG, class conflict, unchanged
    mapping, remove, missing class, warnings, and audit rows. Register the
    shared manager tests in `ginkgo_suite_test.go` (file 50).
*   **Rationale**: Class writes mutate the agent configuration. The RPC layer
    must enforce the VG existence, one-class-per-VG, and gate rules before the
    file is touched.

#### 46. `internal/agent/server_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Use distinct known serials for the revocation test
    certificates (the existing templates already set a serial). Rewrite the
    direct `UnaryInterceptor` calls to `server.unaryInterceptor` or the test
    helper. Pass a fake `RevocationChecker` to every successful `NewServer`
    call. Assert that a revoked serial is rejected on the HTTP and gRPC paths.
    Add a non-revoked control case.
*   **Rationale**: The server is where both auth paths meet. This test catches
    a check that is wired into only one transport.

#### 47. `internal/agent/server_more_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Update every successful `NewServer` setup to pass a
    fake `RevocationChecker`. Add health, readiness, and metrics checks with a
    revoked certificate.
    Assert the response is 401 and the body is a `google.rpc.Status` with
    `CERTIFICATE_REVOKED`. Add the `Cache-Control: no-store` assertion for the
    issue response and for error responses that may carry key material.
*   **Rationale**: Revocation must not leave the unauthenticated-looking
    endpoints open to a revoked credential.

#### 48. `internal/agent/server_integration_test.go` (Modify)

*   **Type / Scope**: mTLS integration test.
*   **Proposed Changes**: Extend the TLS helper to write the CA private key and
    issue an admin certificate, construct the authority with the same CA files
    and a temporary certificate directory, pass it with
    `agent.WithRevocationChecker(authority)` to `NewServer`, then issue a
    certificate, call an admin RPC, revoke it, and assert the next call is
    rejected. Add a restart case that rebuilds the server and cache from the
    same store and passes the rebuilt authority as the checker.
*   **Rationale**: This is the end-to-end proof that issuance and revocation
    work through the real TLS listener, not only through unit fakes.

#### 49. `internal/agent/volume_service_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Add capability values to `ServiceOptions` and assert
    `GetInfo` returns them, including `CallerRole`/`CallerOwner` from the
    authenticated identity. Keep the existing info and list coverage.
*   **Rationale**: The provider's configure path depends on this field.

#### 50. `internal/agent/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Register the new certificate, class, `GetVG`,
    external-revoke, revocation, and capability tests.
*   **Rationale**: New Ginkgo specs must be registered.

#### 51. `internal/adminui/server.go` (Modify)

*   **Type / Scope**: Local admin UI server.
*   **Proposed Changes**: Add the narrow `pki.Issuer`, `pki.Registry`,
    `pki.ExternalRevoker`, and `pki.FileStore` interfaces plus
    `VGClass *agent.VGClassManager`, `Audit *audit.Recorder`, and
    `CAStatus pki.CAStatus` to `Options` and require them in `New`. Remove
    `Options.VGClassGate` and `Server.vgClassGate`; the manager is the sole gate
    owner. Add
    the `DELETE /api/certificates/{serial}` and
    `POST /api/certificates:revoke-external` routes. Route
    `GET /api/certificates` to the registry-aware list, `POST` to the thin
    issue handler, and `GET /api/certificates/{file}` to the safe download
    handler. Remove the old certificate-directory and CA-key path helpers.
    Keep the loopback-only and no-authentication behavior.
*   **Rationale**: The UI must use the same authority as the mTLS API. Removing
    the duplicate CA code is what makes delete and revocation consistent.

#### 52. `internal/adminui/certificate_issue.go` (Modify)

*   **Type / Scope**: Local UI HTTP handler.
*   **Proposed Changes**: Reduce the file to request decoding, role/name/TTL
    extraction, a call to `Issuer.Issue` with source `admin-ui`, and response
    mapping. Remove CA loading, key generation, serial generation, and file
    writes. Remove the duplicated role map and name regex. Remove the current
    `server.writeAudit("certificate_issue", ...)` call and its logging; the pki
    authority writes the certificate audit row.
*   **Rationale**: Private key handling belongs in `internal/pki`, not in an
    HTTP handler.

#### 53. `internal/adminui/certificates.go` (Modify)

*   **Type / Scope**: Local UI HTTP handlers.
*   **Proposed Changes**: Implement list, download, revoke, and external
    revoke. The list loops `Registry.List` until the page token is empty, joins
    the summaries with `FileStore.ListFiles` by serial, includes issuance state
    and TTL policy, and returns the CA file separately. It sets
    `issuance_enabled = config.Enabled && CAStatus.Ready()` and `error =
    CAStatus.CAState()`; disabled issuance still allows list/revoke. The revoke handler accepts a serial and calls `Registry.Revoke`
    with reason `admin-ui`; the external handler accepts a PEM and calls
    `ExternalRevoker.RevokeExternal`. The download handler uses
    `FileStore.ResolveFile`. Move role parsing and sensitive-name filtering out
    of this file.
*   **Rationale**: The UI is the human-facing path for the same registry. It
    must not maintain a second file model.

#### 54. `internal/adminui/types.go` (Modify)

*   **Type / Scope**: UI JSON types.
*   **Proposed Changes**: Add serial, `revoked` status, expiry, revocation, and
    `KeyDownloadURL` fields to `CertificateFile`. The list response uses one
    `CertificateFile` per registry certificate, with `DownloadURL` for the
    certificate and `KeyDownloadURL` for its private key. Add
    `IssuanceEnabled`, `DefaultTTLDays`, and `MaxTTLDays` to
    `CertificatesResponse`, and rename `files` to `certificates`. Add serial,
    expiry, role, name, and `warnings.Warning` values to
    `CreateCertificateResponse`. Add
    `RevokeCertificateResponse` and `RevokeExternalCertificateResponse` with
    `warnings.Warning` values.
*   **Rationale**: The frontend needs the registry state, not just file names,
    to show and revoke certificates.

#### 55. `internal/adminui/config_file.go` (Modify)

*   **Type / Scope**: Configuration editor and restart detection.
*   **Proposed Changes**: Remove the old certificate fields from normalization
    and comparison. Add every `certificate_authority` field to the
    `restart_required` comparison. Keep the existing YAML validation and atomic
    save behavior.
*   **Rationale**: The CA key and TTL policy are loaded at startup. The UI must
    not imply that editing them takes effect immediately.

#### 56. `internal/adminui/adminui_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Provide fake `pki.Issuer`, `pki.Registry`,
    `pki.ExternalRevoker`, `pki.FileStore`, and `pki.CAStatus` implementations,
    plus a real
    `agent.VGClassManager` and `audit.Recorder` backed by fake
    `VGService`/`ClassRegistry`/store objects, in the server options. Rewrite or
    remove the existing certificate tests that set `AdminUI.ClientCertDir` or
    `AdminUI.ClientCAKeyFile`. Add route tests for list, create, revoke,
    external revoke, and download.
    Assert that `config.Parse` rejects the old `admin_ui` certificate fields.
    Keep the existing status, config, VG, class, and target tests.
*   **Rationale**: The route and dependency changes are visible behavior
    changes for the UI.

#### 57. `internal/adminui/certificate_issue_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Replace direct crypto tests with handler tests for
    issue, list, revoke, disabled issuance, unknown serial, and warnings. Move
    the CA, key-generation, and file tests to `internal/pki`.
*   **Rationale**: The crypto now lives in `internal/pki`; keeping duplicate
    tests in the UI package would test the wrong layer.

#### 58. Generated `internal/adminui/assets/` bundle (Generated, Modify)

*   **Type / Scope**: Embedded web bundle.
*   **Proposed Changes**: Run `make build` after the `web/src` changes. Commit
    the regenerated `index.html`, hashed JS, and CSS files. Do not edit them by hand.
*   **Rationale**: The agent embeds the UI. A source change that is not rebuilt
    is not shipped. `make build-go` still verifies the Node-free fallback.

#### 59. `web/src/api.ts` (Modify)

*   **Type / Scope**: Frontend API client.
*   **Proposed Changes**: Widen `createCertificate` to all four roles and an
    optional `ttl_days`. Add `revokeCertificate(serial)` and
    `revokeExternalCertificate({certificate_pem, reason})`. Keep
    `certificateDownloadURL` for file downloads.
*   **Rationale**: The panel needs the new role and revoke operations.

#### 60. `web/src/types.ts` (Modify)

*   **Type / Scope**: Frontend types.
*   **Proposed Changes**: Add the certificate registry, revocation,
    key-download, and external-revoke response fields described in file 54.
    Keep the existing VG and class types unchanged.
*   **Rationale**: The frontend must compile against the new JSON shapes.

#### 61. `web/src/components/CertificatesPanel.tsx` (Modify)

*   **Type / Scope**: Frontend component.
*   **Proposed Changes**: Render registry rows with file name, serial, role,
    status, expiry, size, modified time, and certificate/key download links.
    Add a revoke button with a confirmation dialog and a "Revoke external
    certificate" form that accepts a PEM. Disable create when
    `issuance_enabled` is false; keep revoke enabled for existing registry
    rows. Keep the CA download link and the private-key warning. Do not add a separate unmanaged-files table or a TTL
    editor.
*   **Rationale**: The panel is the human interface for the new delete/revoke
    behavior. It must not imply that arbitrary filesystem files are revocable;
    external revocation requires a verified PEM.

#### 62. `web/src/components/StateBadge.tsx` (No change; not a target)

*   **Type / Scope**: Frontend component; listed only for scope clarity.
*   **Proposed Changes**: Not a target. The certificate panel renders plain
    status text; it does not use the state badge for certificate rows.
*   **Rationale**: The reduced UI scope does not add a new badge state.

#### 63. `cmd/kouja-agent/main.go` (Modify)

*   **Type / Scope**: Agent startup.
*   **Proposed Changes**: After the state store is open, construct one
    `auditRecorder := audit.NewRecorder(store, logger, metrics)` and one
    `validators := naming.NewValidators()`. Construct the `pki.Service` with
    `pki.Options{Audit: auditRecorder, Naming: validators, ...}`. Construct
    `gate := &sync.Mutex{}` and `manager :=
    agent.NewVGClassManager(vgs, classRegistry, gate, auditRecorder)`; pass only
    the manager to `agent.WithVGClassManager` and `adminui.Options.VGClass`,
    never the raw gate. Pass `auditRecorder` to `adminui.Options.Audit`, and
    pass the `pki.Service` as `agent.WithCertificateService`, the UI's
    `Issuer`/`Registry`/`ExternalRevoker`/`FileStore`/`CAStatus` fields, and
    `agent.WithRevocationChecker`. Set `agent.ServiceOptions.Capabilities` to
    `certificates` and `storage_classes`. Start `service.Run(ctx)` and include
    its completion in the shutdown path. Only a
    `certificate_authority.key_file` or `certificate_dir` failure degrades
    issuance; a missing/invalid/expired `listen.tls.client_ca_file`, invalid
    config, or unreadable state store/journal is a startup error. Change
    `runBackups` to accept `backup.Options{JournalSnapshotter: pkiService,
    RevocationLogName: "revoked-serials.log", ...}` and pass those options into
    the goroutine; the source path stays in `certificate_authority` config.
*   **Rationale**: This is the composition root. A missing wiring line would
    leave the API or UI with a nil authority or would leave the revocation
    cache unused.

#### 64. `cmd/terraform-provider-kouja/main.go` (Create)

*   **Type / Scope**: Provider entrypoint.
*   **Proposed Changes**: Define `var version = "dev"`, serve the provider with `providerserver.Serve`,
    address `registry.terraform.io/voidstarit/kouja`, and pass the build version
    from `-ldflags`. Support the standard `-debug` flag for local development.
*   **Rationale**: Terraform requires a plugin entrypoint with the provider
    address and protocol version. Keeping it in `cmd/` matches the repository's
    binary layout.

#### 65. `internal/terraformprovider/provider.go` (Create)

*   **Type / Scope**: Provider metadata and configuration.
*   **Proposed Changes**: Implement `Metadata` with type name `kouja` and the
    build version. Implement `Schema` with the provider attributes from the
    Technical Plan. Implement `Configure` to resolve PEM/file pairs, build the
    client, check `GetInfo` capabilities and `caller_role`, and store the
    provider meta. Register the three resources.
*   **Rationale**: This is the provider's composition point. The capability and
    role checks turn configuration mistakes into immediate diagnostics.

#### 66. `internal/terraformprovider/config.go` (Create)

*   **Type / Scope**: Provider configuration model.
*   **Proposed Changes**: Define the Terraform model for `endpoint`, `server_ca_*`, `client_cert_*`,
    `client_key_*`, `server_name`, and `timeout`. Implement HCL > env > default
    precedence, PEM/file mutual exclusion, HTTPS endpoint validation, client
    certificate/key match, TLS 1.3 configuration through
    `client.LoadTLSConfig`/`client.WithTLSConfig`, and context-deadline
    timeouts. Do not build a separate HTTP transport.
*   **Rationale**: The provider must support both inline PEM and file paths
    without duplicating this logic in every resource.

#### 67. `internal/terraformprovider/client.go` (Create)

*   **Type / Scope**: Provider API client.
*   **Proposed Changes**: Define `providerData` and the `providerClient`
    interface. Implement the `GetInfo` capability and `caller_role` check,
    `GetVG`, request-ID propagation for VG/class/certificate mutations, the
    shared reason-to-diagnostic mapping, read retry/backoff, and parsing of the
    provider's own certificate serial and `not_after` for the self-revoke guard
    and near-expiry warning. `RegisterVG` passes an empty expected UUID and the
    operation request ID. `IssueCertificate` is never retried.
*   **Rationale**: A small wrapper keeps resource code focused on Terraform
    state and lifecycle, not transport details.

#### 68. `internal/terraformprovider/resource_volume_group.go` (Create)

*   **Type / Scope**: Terraform resource.
*   **Proposed Changes**: Implement `kouja_volume_group` with the schema and
    CRUD behavior from the Technical Plan. Create calls `RegisterVG` with an empty expected UUID. Read calls `GetVG`
    and reports UUID drift; `NotFound` removes state. Delete calls
    `DeregisterVG`; `NotFound` is success; `VG_IN_USE`/`VG_CLASS_MAPPED` are
    diagnostics. Support import by VG name.
*   **Rationale**: This is the declarative replacement for manual VG
    registration.

#### 69. `internal/terraformprovider/resource_storage_class.go` (Create)

*   **Type / Scope**: Terraform resource.
*   **Proposed Changes**: Implement `kouja_storage_class` with the schema and
    CRUD behavior from the Technical Plan. Create and Update call `SetClass`.
    Read calls `GetClass` and removes the resource on `NotFound`. Delete calls
    `RemoveClass` and treats `NotFound` as success. Support import by class name.
*   **Rationale**: Class mappings are the selection layer for volume placement.
    Terraform needs per-mapping identity and conflict diagnostics.

#### 70. `internal/terraformprovider/resource_client_certificate.go` (Create)

*   **Type / Scope**: Terraform resource.
*   **Proposed Changes**: Implement `kouja_client_certificate` with the schema
    and CRUD behavior from the Technical Plan. Generate a request ID for audit correlation per create, call
    `IssueCertificate` without retrying, validate all returned PEMs/serial, map
    every `IssueCertificateResponse.warnings` entry to a Terraform warning
    diagnostic, set the serial as the resource ID, and store the returned
    PEMs. Read calls
    `GetCertificate`; `NotFound`, expired, or `revoked=true` removes the
    resource; `private_key_pem` is copied from prior state and a missing key
    removes the resource. Delete calls `RevokeCertificate`, treats `NotFound` as
    success, and refuses to revoke the provider's own serial. Do not implement
    import.
*   **Rationale**: This is the core Terraform credential lifecycle. The
    self-revoke guard prevents a single apply from locking out the provider.

#### 71. `internal/terraformprovider/provider_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover metadata, schema validation, PEM/file
    resolution, endpoint parsing, timeout defaults, missing capability
    diagnostics, non-admin diagnostics, the self-serial parse, read retry,
    no-retry on `IssueCertificate`, incomplete keypair rejection, warning
    diagnostics including a `durability_uncertain` issue response, a 60-day
    agent default with omitted `ttl_days`, system-roots TLS, mixed PEM/file
    pairs, and the near-expiry warning.
*   **Rationale**: Configuration errors are the most common provider failure.
    They should be caught before a resource plan.

#### 72. `internal/terraformprovider/resource_volume_group_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover schema validation, state mapping, diagnostic
    messages for UUID mismatch, VG-in-use, and class-mapped errors, and import
    parsing.
*   **Rationale**: VG deletion is blocked by real dependencies. The diagnostics
    must tell the operator what to remove.

#### 73. `internal/terraformprovider/resource_storage_class_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover schema validation, state mapping, VG update,
    class conflict, missing class on read, and import parsing.
*   **Rationale**: Class conflicts and drift are the likely failure modes. They
    need precise messages.

#### 74. `internal/terraformprovider/resource_client_certificate_test.go` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Cover schema validation, sensitive attributes,
    replacement triggers, read-removes-revoked behavior, self-revoke refusal,
    and missing-certificate delete behavior.
*   **Rationale**: Certificate lifecycle mistakes can delete a working
    credential or leave a key in the wrong state.

#### 75. `internal/terraformprovider/acceptance_test.go` (Create)

*   **Type / Scope**: Acceptance tests.
*   **Proposed Changes**: Add `TestAccKoujaVolumeGroup`, `TestAccKoujaStorageClass`,
    and `TestAccKoujaClientCertificate` using `terraform-plugin-testing` and
    `testutil_test.go`. Guard with `TF_ACC=1` and the `acceptance` build tag.
    Cover create, read, update, delete, import (VG and class), replacement
    (certificate), conflict diagnostics, self-revoke guard, and expired/revoked
    read behavior. The fake server implements `GetInfo`, `ListVGs`, `GetVG`, and
    the VG/class/certificate RPCs, requires a client certificate, and rejects a
    caller certificate.
*   **Rationale**: Unit tests cannot prove Terraform's plan/apply/refresh
    behavior. The acceptance tests exercise the real plugin protocol against a
    controlled TLS server.

#### 76. `internal/terraformprovider/testutil_test.go` (Create)

*   **Type / Scope**: Test support.
*   **Proposed Changes**: Generate a temporary CA, server certificate, admin client certificate, and
    the provider client certificate (CA-issued, not self-signed). Start an
    `httptest.NewUnstartedServer`, set
    `TLS.ClientAuth=tls.RequireAndVerifyClientCert` with the generated CA in
    `ClientCAs`, install the generated server certificate, then `StartTLS`. The
    server implements the admin endpoints with the generated proto types and
    `protojson`. Provide the provider configuration with inline PEM values.
    Track VGs, classes, certificates, and revocations in memory.
*   **Rationale**: The provider tests need a deterministic agent without LVM,
    root, or a real network host. Using the generated proto types keeps the
    JSON field names correct.

#### 77. `internal/terraformprovider/ginkgo_suite_test.go` (Create)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Add a suite runner for the provider unit tests. It
    has no separate build tag, matching the existing unit test packages. The
    acceptance file uses `//go:build acceptance`; `testutil_test.go` stays
    untagged so both suites can use it.
*   **Rationale**: The unit tests follow the repository's Ginkgo convention.
    The acceptance tests stay standard `testing` because
    `terraform-plugin-testing` requires `*testing.T`.

#### 78. `cmd/terraform-provider-kouja/README.md` (Create)

*   **Type / Scope**: Provider README.
*   **Proposed Changes**: Document the build command, filesystem-mirror and
    `dev_overrides` installation, the link to `docs/terraform.md`, the state
    security warning, and the bootstrap admin certificate requirement.
*   **Rationale**: A Terraform provider needs an installation path that does not
    depend on the public registry.

#### 79. `cmd/terraform-provider-kouja/examples/provider/provider.tf` (Create)

*   **Type / Scope**: Example configuration.
*   **Proposed Changes**: Show the provider block with endpoint, server CA,
    admin certificate/key paths, and server name variables.
*   **Rationale**: This is the first thing an operator copies. It must show the
    correct CA roles and avoid embedding secrets.

#### 80. `cmd/terraform-provider-kouja/examples/volume-group/main.tf` (Create)

*   **Type / Scope**: Example configuration.
*   **Proposed Changes**: Show a `kouja_volume_group` with computed outputs.
*   **Rationale**: Demonstrates the minimal VG workflow.

#### 81. `cmd/terraform-provider-kouja/examples/storage-class/main.tf` (Create)

*   **Type / Scope**: Example configuration.
*   **Proposed Changes**: Show a VG and a dependent `kouja_storage_class`.
*   **Rationale**: Demonstrates the dependency order and the one-class-per-VG
    rule.

#### 82. `cmd/terraform-provider-kouja/examples/client-certificate/main.tf` (Create)

*   **Type / Scope**: Example configuration.
*   **Proposed Changes**: Show a caller certificate, the sensitive outputs, and
    a comment recommending `create_before_destroy` and encrypted state.
*   **Rationale**: Demonstrates the credential lifecycle and the state warning.

#### 83. `go.mod` (Modify)

*   **Type / Scope**: Go module dependencies.
*   **Proposed Changes**: Pin the Terraform Plugin Framework v1, framework validators v0, and
    terraform-plugin-testing v1 lines at implementation time and record the
    resolved exact versions in `go.mod`. `go mod tidy` adds the indirect graph.
*   **Rationale**: The provider cannot compile without these modules. The
    provider is in the existing module, so the dependencies live in the main
    `go.mod`.

#### 84. `go.sum` (Modify)

*   **Type / Scope**: Dependency checksums.
*   **Proposed Changes**: Regenerate with `go mod tidy`.
*   **Rationale**: Reproducible builds require the checksum entries.

#### 85. `Makefile` (Modify)

*   **Type / Scope**: Build and test automation.
*   **Proposed Changes**: Add `build-provider`, `test-provider`,
    `lint-provider`, and `provider-acceptance` targets. Add the provider binary
    to `binaries` so `make all` produces it. Add the new targets to `.PHONY`.
    Keep `GO_PACKAGES` as is; it already includes `./cmd/...` and
    `./internal/...`. The target commands are:
    *   `build-provider`: `CGO_ENABLED=0 go build -trimpath
        -ldflags "-X main.version=$(VERSION)" -o bin/terraform-provider-kouja
        ./cmd/terraform-provider-kouja`, with `VERSION ?= $(shell git describe
        --tags --always --dirty)`.
    *   `test-provider`: `go test ./internal/terraformprovider/...`.
    *   `lint-provider`: `go vet ./internal/terraformprovider/...
        ./cmd/terraform-provider-kouja/...` followed by `golangci-lint run`
        on the same packages.
    *   `provider-acceptance`: `TF_ACC=1 go test -tags=acceptance
        ./internal/terraformprovider/... -run TestAcc -v`.
*   **Rationale**: The repository's release layout and CI use `make`. The
    provider must be a first-class build artifact, and the acceptance tests
    need a separate target because they require the Terraform CLI.

#### 86. `.github/workflows/ci.yml` (Modify)

*   **Type / Scope**: CI.
*   **Proposed Changes**: Keep the existing test and quality jobs. Add a
    `provider` job that runs the unit tests, installs Terraform, runs the
    `acceptance`-tagged tests with `TF_ACC=1`, and runs `terraform fmt -check`
    on the examples. Add a `proto` job that installs the pinned generators with
    `make tools`, runs `make proto`, and fails on `git diff --exit-code`.
    Ensure the existing `make all` builds the provider binary.
*   **Rationale**: The provider tests need Terraform, which the existing jobs do
    not install. The proto check catches hand-edited or stale generated code.
    Separate jobs keep the agent test job fast and isolate the extra tools.

#### 87. `deploy/config.example.yaml` (Modify)

*   **Type / Scope**: Example agent configuration.
*   **Proposed Changes**: Remove `admin_ui.client_cert_dir` and
    `admin_ui.client_ca_key_file`. Add the `certificate_authority` section with
    `enabled: false`, the default paths, and the configured default/maximum TTL
    values (30/365 unless overridden). Add comments
    that the CA key must be installed and protected before enabling.
*   **Rationale**: Operators copy this file. It is the primary discovery path
    for the new configuration.

#### 88. `deploy/install/install.sh` (Modify)

*   **Type / Scope**: Installer.
*   **Proposed Changes**: Update the starter configuration heredoc to the new
    section and remove the old fields. Update the post-install message to point
    at the CA key installation and the enablement step. Do not create
    certificates or the CA key automatically.
*   **Rationale**: The installer currently writes the old UI fields. If it is
    not updated, a fresh install produces a configuration that the new agent
    rejects.

#### 89. `deploy/kouja-init.sh` (Modify)

*   **Type / Scope**: Interactive host initializer.
*   **Proposed Changes**: Write the `certificate_authority` section with the generated `CA_KEY` and
    `$TLS_DIR/clients`. Set `enabled: true` only when the script actually
    generated the CA key and the admin certificate; otherwise write
    `enabled: false` and print the enablement step. Remove the old admin UI certificate fields. Keep the existing
    CA, server, and admin certificate generation. Generate client/admin certificates with `openssl x509 -days 365` so the
    initializer does not contradict the configured maximum. Document that the initializer's admin
    certificate is an unmanaged bootstrap credential. The installer-only path
    remains disabled until the operator supplies the key.
*   **Rationale**: The initializer is the bootstrap path for the provider's
    admin certificate. It must produce a configuration the agent accepts, and
    the operator has explicitly chosen to create the CA material.

#### 90. `README.md` (Modify)

*   **Type / Scope**: Project documentation.
*   **Proposed Changes**: Add a Terraform section that links to
    `docs/terraform.md`; update the admin UI configuration and certificate
    panel description; update the API quick start with the new admin endpoints;
    update the security notes about admin certificate issuance, revocation,
    private key exposure in Terraform state, and the single-administrator UI
    precondition; update the certificate file naming and TTL.
*   **Rationale**: The README is the public entry point. It must not describe
    the removed `admin_ui` fields or the old 825-day issuance behavior.

#### 91. `docs/clients.md` (Modify)

*   **Type / Scope**: Client SDK documentation.
*   **Proposed Changes**: Add examples for the new admin client methods, the
    request-ID handling for certificate issuance, the revocation call, and the
    class mapping calls. Mention the capability field in `GetInfo`.
*   **Rationale**: The generated client is the supported API surface for Go
    consumers. The new RPCs need examples and idempotency guidance.

#### 92. `docs/client-errors.md` (Modify)

*   **Type / Scope**: Error and retry documentation.
*   **Proposed Changes**: Add the new reason table entries from the Technical
    Plan. Add `GetCertificate`, `ListCertificates`, `GetVG`, `ListClasses`, and
    `GetClass` to the read-only retry list. State that `IssueCertificate` is not
    retried and its request ID is audit correlation only. Add
    `RevokeCertificate`, `RevokeExternalCertificate`, `SetClass`, and
    `RemoveClass` to the idempotent mutation guidance. State that
    `CERTIFICATE_REVOKED` is never retryable.
*   **Rationale**: The retry table is the contract the provider and other
    clients use. An omitted reason leads to blind retries or unnecessary
    failures.

#### 93. `docs/terraform.md` (Create)

*   **Type / Scope**: Terraform user guide.
*   **Proposed Changes**: Add prerequisites, bootstrap admin certificate
    steps, provider installation without the public registry, provider
    configuration, the three resources with examples, state security, the
    certificate lifecycle, revocation behavior, the self-revoke guard, the
    capability check, troubleshooting, and the manual QEMU verification.
*   **Rationale**: The provider cannot be published to the public registry, so
    the installation and bootstrap instructions must be explicit and complete.

#### 94. `docs/designs/admin-ui.md` (Modify)

*   **Type / Scope**: Admin UI design record.
*   **Proposed Changes**: Update the configuration section to the
    `certificate_authority` section, remove the old fields, update the
    certificate API table with the registry list, all four roles, TTL, and the
    delete route, update the certificate panel description, and add the
    startup-bound restart rules for the new section.
*   **Rationale**: The admin UI design record is the reference for the UI's
    security model and routes. It must match the implemented behavior.

#### 95. `docs/designs/kouja.md` (Modify)

*   **Type / Scope**: Core design record.
*   **Proposed Changes**: Update the security model with agent-issued client
    certificates, the `spiffe://kouja/<role>/<name>` SANs, the revocation
    cache, the certificate registry, schema version 2, and the
    `certificate_authority` configuration. Add the new admin RPCs to the API
    list. Add a superseding note that the agent can now issue and revoke
    client certificates in addition to accepting operator-issued certificates.
*   **Rationale**: The core design currently says client certificates are
    issued by the operator's CA. Leaving that statement unchanged would make
    the security documentation wrong.

#### 96. `docs/e2e.md` (Modify)

*   **Type / Scope**: End-to-end validation guide.
*   **Proposed Changes**: Add a Terraform provider section to the QEMU/manual
    validation. Cover building the provider, bootstrapping the admin
    certificate, applying the examples, verifying VG registration, class
    selection, certificate use, revocation, and the rejection of the revoked
    certificate. Include the expected failure when deregistering a class-mapped
    VG.
*   **Rationale**: The QEMU guide is the real-host verification path. It must
    exercise the new surfaces, not only the volume data path.

#### 97. `docs/designs/terraform.md` (Modify, finalization)

*   **Type / Scope**: Published design record.
*   **Proposed Changes**: This file is the authoritative final design. If `design_draft.md` remains
    at the repository root, mark it as superseded rather than copying over this
    file. No `docs/designs/SITEMAP.md` exists, so no sitemap entry is added.
*   **Rationale**: This is the durable design record referenced by future
    implementation and review sessions.

### Implementation Plan Resolution Addendum

The following files and contract changes were added or corrected by the review
resolution pass. They supersede the corresponding entries above.

| File | Action | Change |
|---|---|---|
| `internal/audit/audit.go` | Create | Shared `Recorder`, `Event`, operation-ID generation, and `RecordTx`. |
| `internal/audit/audit_test.go` | Create | Field mapping, operation ID, transaction failure, one row per mutation. |
| `internal/audit/ginkgo_suite_test.go` | Create | Suite registration. |
| `internal/warnings/warnings.go` | Create | `Warning{Code, Message}` and the `durability_uncertain`, `audit_write_failed`, `file_delete_failed` codes. |
| `internal/warnings/warnings_test.go` | Create | Code/message mapping. |
| `internal/fsutil/atomic.go` | Modify | Add `WriteFileExclusiveAtomic` for certificate/key files. |
| `internal/pki/testutil_test.go` | Create | Shared CA, store, audit, clock, and temporary-directory helper. |
| `internal/agent/admin_vg_class.go` | Create | Shared `VGClassManager` used by the mTLS API and local UI. |
| `internal/agent/admin_vg_class_test.go` | Create | Gate, VG existence, warning, audit, and concurrency tests. |
| `gen/client/openapi_test.go` | Modify | Add the new paths/operation IDs, including `{serialNumber}`, `{className}`, and `{vgName}`. |
| `gen/client/http_test.go` | Modify | Add cases for the new generated HTTP methods. |
| `docs/upgrading.md` | Create | Breaking changes, pre-v2 backup, rollback, external revoke, and CA-rotation fallback. |
| `internal/state/credentials.go` | Modify | Phase 1: add `PutAuditEvent` and `DeleteAuditBefore`; keep `PutAudit` unchanged. |
| `internal/agent/vg_service.go` | Modify | Add audit-aware `RegisterWithAudit`/`DeregisterWithAudit` that write the VG row and audit row in one `VGManager` transaction; existing `Register`/`Deregister` remain as compatibility wrappers. |
| `internal/agent/vg_class_test.go` | Modify | Update direct manager calls only where the audit-aware API is required; keep the existing gate tests. |
| `internal/adminui/vg.go` | Modify | Thin adapter over `agent.VGClassManager`; remove the four `writeAudit` calls and propagate the manager's warnings. |
| `internal/adminui/audit.go` | Modify | Delegate to `internal/audit.Recorder`. |
| `internal/adminui/assets/index.html` | Modify | Vite rewrites the hashed asset links. |

Contract corrections that supersede the entries above:

*   Certificate issuance is non-idempotent; `request_id` is nullable, non-unique,
    and used only for audit correlation.
*   `certificates` has `issued_by` and a nullable `request_id`.
*   The audit table has explicit resource type, resource ID, role, name, and
    detail columns; `PutAuditEvent` and `state.AuditEvent` carry them. The
    legacy `PutAudit` keeps its existing validation and SQL unchanged.
*   Revocation appends to the journal before SQLite, cache, and file changes.
*   `GetCertificate` returns `CERTIFICATE_NOT_FOUND` for expired rows.
*   `ListCertificates` is paginated; `GetVG` exists; the provider uses `GetVG`.
*   The provider uses `server_ca_*`, optional `ttl_days` (0 means default), and
    the `GetVG` drift check.
*   The UI lists registry rows only and uses the narrow pki interfaces.
*   `deploy/kouja-init.sh` enables issuance only when the CA key and admin
    certificate were actually created.

### Second Review Resolution Addendum

The second review pass added or corrected these contracts and files. They
supersede the corresponding entries above.

| File | Action | Change |
|---|---|---|
| `internal/naming/naming.go` | Create | Shared certificate, VG, and class name validators used by config, agent, admin UI, and provider. |
| `internal/naming/naming_test.go` | Create | Regex and boundary tests. |
| `internal/backup/backup.go` | Modify | Add `JournalSnapshotter`, copy the journal, record it in the manifest, and retain it with the database. |
| `internal/backup/backup_test.go` | Modify | Journal snapshot, manifest, retention, and older-database coverage; restore is the documented manual procedure. |
| `internal/agent/certificate_admin.go` | Create | Add `RevokeExternalCertificate`; external PEM verification and revocation-only row. |
| `internal/adminui/server.go` | Modify | Add the shared `VGClassManager` field, the external-revoke route, and the error/quota mappings. |
| `internal/adminui/types.go` | Modify | Add `CertificateSummary`-shaped list rows, create warnings, and external-revoke response. |
| `internal/state/certificates.go` | Create | Store `client_ca_pem`, `issuer_serial`, and count active (unrevoked, unexpired) rows. |
| `internal/state/types.go` | Modify | Add `ClientCAPEM` and `IssuerSerial` fields. |
| `internal/config/config.go` | Modify | Add `revocation_log_file` path checks and active-certificate quota validation. |
| `internal/terraformprovider/client.go` | Create | Attach mutation request IDs with `client.WithRequestID`, use `GetInfo.caller_role`, and support system roots/server name. |
| `internal/terraformprovider/resource_client_certificate.go` | Create | Persist/copy `client_ca_pem`, validate all PEMs, and warn near bootstrap expiry. |
| `docs/upgrading.md` | Create | Schema backup, old config, CA rotation, external-certificate revocation, and rollback limits. |

Contract corrections:

*   Journal entries include `not_after`; append and compaction share one mutex;
    a partial tail fails append; malformed lines enter degraded mode.
*   The extended `internal/backup.Manager` snapshots the journal through
    `JournalSnapshotter`; restore is the documented manual procedure.
*   `state.Open` always creates a new `UnixNano`-timestamped pre-v2 backup and
    fails startup if it cannot be produced.
*   `RevokeExternalCertificate` covers orphans and pre-existing certificates.
*   Certificate rows store `client_ca_pem` and `issuer_serial`.
*   `ListCertificates` returns summaries; quota counts only unrevoked, unexpired
    rows and is serialized by the issuance mutex.
*   `GetInfo` returns `caller_role`/`caller_owner`; the provider uses
    `caller_role` for its role probe.
*   `Cache-Control: no-store` uses an outer middleware or custom error handler
    so it covers issue error responses.
*   The audit field mapping and best-effort failed-row policy are canonical in
    §5.5; class no-op writes no mutation row.
*   The UI uses the shared `VGClassManager`; `RevokeExternalCertificate` is
    exposed as a form that accepts a PEM, not as an unmanaged-file table.

### Final Review Resolution Addendum

The final pass adds these files and contract assignments:

| File | Action | Change |
|---|---|---|
| `internal/pki/journal.go` | Create | JSON-lines journal append/load/compact/health and `SnapshotJournal` under one mutex; partial/malformed lines return `ErrJournalUnhealthy`; `not_after` entries; directory fsync on create/compaction. |
| `internal/pki/journal_test.go` | Create | Append/compaction race, malformed trailing line, journal-ahead/journal-orphan and SQLite-revoked/journal-missing reconciliation, manual restore. |
| `internal/pki/external.go` | Create | `ExternalRevoker` implementation: PEM verification, journal-first ordering, revocation-only row, orphan key cleanup. |
| `internal/pki/external_test.go` | Create | Existing row delegation, truly external cert, journal-orphan, invalid PEM, duplicate serial, orphan key deletion. |
| `internal/backup/backup.go` | Modify | Add `JournalSnapshotter`/`RevocationLogName` to `Options`, the manifest, and `Rotate`; snapshot and validate the journal. |
| `internal/backup/backup_test.go` | Modify | Journal snapshot, manifest, retention, partial-line handling; manual restore procedure. |
| `internal/naming/naming.go` | Create | `CertificateName`, `VolumeGroupName`, and `StorageClassName` validators; certificate and VG/class regexes are distinct. |
| `internal/config/config.go`, `internal/config/storage.go`, `internal/agent/admin_service.go`, `internal/agent/storage_class.go`, `internal/agent/vg_service.go`, `internal/adminui/certificate_issue.go`, `internal/lvm/backend.go` | Modify | Replace the duplicated validators with `internal/naming`. |
| `internal/terraformprovider/config.go` | Create | Replace the duplicated validators with `internal/naming`. |
| `internal/state/store.go` | Modify | Pre-v2 `Store.Backup` with timestamped name; `:memory:` skip. |
| `internal/state/certificates.go` | Create | Empty-string file fields, client CA/issuer serial, pagination tokens, active count. |
| `internal/agent/volume_service.go` | Modify | Populate `CallerRole`/`CallerOwner` from `auth.Identity`. |
| `internal/agent/admin_service.go` | Modify | `WithCertificateService`, `WithVGClassManager`, `GetVG`, `vgStatusError` reasons; remove `WithVGClassGate`. |
| `internal/adminui/server.go` | Modify | `VGClass` option, external revoke route, outer no-store middleware. |
| `internal/adminui/types.go` | Modify | External-revoke response, `certificates` list, warnings. |
| `web/src/api.ts`, `web/src/types.ts`, `web/src/components/CertificatesPanel.tsx` | Modify | External-revoke API/type/form; list join metadata; quota/error mapping. |
| `cmd/kouja-agent/main.go` | Modify | Pass `pki.Service` as `JournalSnapshotter` and the journal target to backup; pass external/file store to admin service/UI. |
| `docs/upgrading.md` | Create | Numbered as file 98 in the execution list. |

Phase assignment is defined by the per-file table in Implementation Order:
Phase 1 covers files 1–15, the additive parts of 16/17, 19, 20, and 27; Phase 2
covers 18, 21–63, 87–89, and the removal parts of 16/17; Phase 3 covers 64–86;
Phase 4 covers 90–98. No addendum file is left without a phase.

#### 98. `docs/upgrading.md` (Create)

*   **Type / Scope**: Upgrade and rollback guide.
*   **Proposed Changes**: Record the schema v2 backup name, the old-config
    restore step, the `admin_ui` field removal, the certificate file-name and
    TTL changes, the `replace=false` VG behavior, external certificate
    revocation, the journal backup/restore procedure, and the rollback
    limitation. Include the minimal client CA rotation runbook: generate a new
    client CA; install it as `listen.tls.client_ca_file` and
    `certificate_authority.key_file`; restart the agent; reissue every client
    certificate. Because the agent has one `client_ca_file` used for both
    trust and signing, rotation is no-overlap: the old and new CAs are not kept
    in the same file. Old clients fail until reissued. State that agent
    rollback after a revocation is unsupported without this rotation, and that
    restoring the pre-v2 backup discards post-upgrade database changes.
*   **Rationale**: The rollback path depends on artifacts and preconditions
    that must be written down before an operator upgrades.

### Deleted Files

None. The old admin UI certificate crypto is moved into `internal/pki`; no
source file is removed.

### Implementation Verification

*   **Formatting and static checks**:
    *   `gofmt -l $(find . -name '*.go' -not -path './vendor/*')` returns no
        files.
    *   `go vet ./...`.
    *   `golangci-lint run`.
    *   `terraform fmt -check -recursive cmd/terraform-provider-kouja/examples`.
*   **Protocol generation**:
    *   `make tools` installs the pinned generators.
    *   `make proto` runs `buf lint` and `buf generate`.
    *   The generated files in `gen/` match the proto changes.
    *   `git diff --exit-code gen/` passes after regeneration in CI.
*   **Unit tests**:
    *   `make test` runs the agent, state, config, auth, pki, admin UI, and
        provider unit tests.
    *   `make test-provider` runs the provider unit tests alone.
    *   `make coverage-check` keeps internal coverage at or above 80%.
*   **Provider acceptance tests**:
    *   `make provider-acceptance` runs the `acceptance`-tagged tests with
        `TF_ACC=1` against the TLS fake server.
    *   The tests cover create, read, update, delete, import for VGs and
        classes, certificate replacement, revocation, conflict diagnostics, and
        the self-revoke guard.
*   **mTLS integration tests**:
    *   The `internal/agent/server_integration_test.go` test issues a
        certificate, calls the API with it, revokes it, and gets
        `CERTIFICATE_REVOKED` on the next request.
    *   A restart variant rebuilds the server and cache from the same database
        and proves the revoked serial is still rejected.
    *   Health, readiness, and metrics requests with a revoked certificate
        return 401.
*   **Retention tests**:
    *   An expired registry row is pruned by the cleanup loop.
    *   Its files and cache entry are removed.
    *   Its audit row remains.
*   **UI tests**:
    *   The admin UI route tests cover list, create, revoke, download, disabled
        issuance, unknown serial, revoked rows, and CA unavailable.
    *   The browser check covers the revoke confirmation, the revoked status,
        the TTL display, and the CA download.
*   **Manual QEMU end-to-end**:
    *   Follow the updated `docs/e2e.md` against a real agent VM.
    *   Confirm the provider can register a real VG, map a class, issue a
        caller certificate, use it, revoke it, and fail the next call.
    *   Confirm a class-mapped VG cannot be deregistered.
    *   Confirm a VG UUID mismatch returns `AlreadyExists`.
*   **Rollback check**:
    *   Start the new agent against a version 1 database and confirm the
        migration to version 2 succeeds.
    *   Attempt to start an older binary against the version 2 database and
        confirm it refuses to start.
    *   Restore the pre-upgrade backup and confirm the older binary starts.
*   **Security checks**:
    *   Confirm the CA private key is never present in the certificate list,
        the download routes, the API responses, the audit rows, or the logs.
    *   Confirm the certificate directory cannot be the TLS root.
    *   Confirm a revoked admin certificate cannot call any admin RPC.
    *   Confirm the provider refuses to revoke its own certificate.

--------------------------------------------------------------------------------

<!-- Provenance: Indicates initial structural drafting via EGM. -->

*Drafted with EGM*
