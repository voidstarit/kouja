# Backup and recovery

`kouja-backup` writes a consistent SQLite snapshot to local storage. When S3 is
enabled, it uploads the full recovery set as individual files. The S3 set is
not wrapped in an opaque archive, so operators can inspect and restore selected
content directly.

## Backup contents

Each S3 backup contains the files needed to restore the agent:

- `state.db` — consistent SQLite snapshot
- `secret.key` — credential encryption key
- `host-id` — host identity
- `revoked-serials.log` — revocation journal snapshot
- `config.yaml` — active configuration file
- `emergency.log` — emergency log, if present
- `tls/server.crt`, `tls/server.key`, `tls/client-ca.crt`
- `ca/ca.key` — certificate-authority signing key
- `certificates/` — issued certificate and key files
- `restore.json` — manifest listing uploaded files and SHA-256 checksums

Local retention follows `state.backup_retention_short`, `_daily`, and `_weekly`.

## S3 configuration

```yaml
state:
  backup_interval_seconds: 900
  s3_backup:
    enabled: true
    bucket: kouja-backups
    prefix: kouja/
    region: eu-central-1
    endpoint: ""              # optional S3-compatible endpoint
    force_path_style: false   # true for MinIO/path-style endpoints
    server_side_encryption: AES256
    kms_key_id: ""            # required for aws:kms
    per_backup_folder: true   # 2026Nov091648/state.db
    retention_days: 7         # delete backup objects older than this
    encryption_key_file: ''   # 32-byte key; encrypt each object client-side
```

Credentials use the standard AWS default chain: environment variables, shared
credentials, instance profile, IRSA, or another configured provider. The
backup worker needs `s3:PutObject`, `s3:ListBucket`, and `s3:DeleteObject` for
the configured bucket/prefix. It deletes backup objects older than
`retention_days` itself; bucket lifecycle rules may be used as an additional
safeguard.

With `per_backup_folder: true`, each backup gets its own `YYYYMMMDDHHmm` folder:

```text
kouja/2026Nov091648/state.db
kouja/2026Nov091648/config.yaml
kouja/2026Nov091648/certificates/admin.crt
kouja/2026Nov091648/restore.json
```

With `per_backup_folder: false`, files are written directly under the prefix
with a timestamped name:

```text
kouja/kouja-20261109T164800.000000000Z-state.db
kouja/kouja-20261109T164800.000000000Z-config.yaml
kouja/kouja-20261109T164800.000000000Z-restore.json
```

The local staging directory is `state.backup_target` when set, otherwise
`state.backup_dir`. The manifest is uploaded last, so a prefix without a
`restore.json` or `*-restore.json` manifest is an incomplete backup and is not
restored.

## Scheduling

The packaged systemd units run `kouja-backup` as a one-shot service from
`kouja-backup.timer`. The shipped timer runs every 15 minutes, matching the
default `state.backup_interval_seconds`. Adjust
`/etc/systemd/system/kouja-backup.timer` if you change the cadence, then run
`systemctl daemon-reload` and restart the timer.

Run a backup manually:

```sh
sudo kouja-backup --config /etc/kouja/config.yaml
```

The worker uses a read-only SQLite connection for `VACUUM INTO`; it does not
open or migrate the agent database for writing.

## Encryption

If `encryption_key_file` is configured, every uploaded object is encrypted
client-side before it leaves the host. Encrypted objects end in `.enc`:

```text
kouja/2026Nov091648/state.db.enc
kouja/2026Nov091648/config.yaml.enc
kouja/2026Nov091648/restore.json.enc
```

The key file must contain 32 raw bytes, a 32-byte base64 value, or 64 hex
characters. For example:

```sh
install -m 0600 /dev/null /etc/kouja/backup.key
head -c 32 /dev/urandom > /etc/kouja/backup.key
```

The encrypted format starts with `KOUJA1` and uses AES-256-GCM chunks. Keep the
key outside the backup bucket; it is required for restore. Without
`encryption_key_file`, the agent uploads plain content and relies on S3 SSE for
at-rest encryption.

## Restore

Restore reads paths from the backup's `config.yaml`, verifies file checksums
from `restore.json`, decrypts files when needed, and writes the recovery set to
the configured locations.

Stop the agent before applying a restore. Preview the plan first:

```sh
kouja-restore --s3 s3://kouja-backups/kouja/2026Nov091648/
```

Apply the restore:

```sh
sudo kouja-restore --s3 s3://kouja-backups/kouja/2026Nov091648/ \
  --key-file /etc/kouja/backup.key \
  --config /etc/kouja/config.yaml \
  --apply
```

The restore command also accepts a local directory of plain backup objects, such
as an S3 folder downloaded with the AWS CLI, or a legacy `.tar.gz` archive:

```sh
kouja-restore --dir /var/lib/kouja/backups/2026Nov091648 \
  --key-file /etc/kouja/backup.key --apply
kouja-restore --archive kouja-20261109T164800.000000000Z.tar.gz \
  --key-file /etc/kouja/backup.key --apply
```

For S3-compatible storage, add `--endpoint`, `--region`, and
`--force-path-style` as needed. AWS credentials need `s3:GetObject` and
`s3:ListBucket` for the backup prefix.

After applying the restore, keep private files at mode `0600` and the
CA/certificate directory at `0700`, then start the agent and verify `/readyz`,
the admin UI, certificate listing, and a test mTLS request.

The database and revocation journal must be restored together. Restoring an
older database with a newer journal is safe for revocation; restoring a newer
database with an older journal loses revocations made after the journal
snapshot. Re-revoke missing credentials with `RevokeCertificate` or
`RevokeExternalCertificate` after restore.

## Security

The backup set contains the CA private key, TLS private keys, the credential
encryption key, and client certificate private keys. Treat the S3 bucket as
highly sensitive:

- Keep the bucket private and block public access.
- Enable default encryption or provide a KMS key in addition to client-side
  encryption.
- Restrict `s3:PutObject`/`s3:GetObject` to the backup worker and recovery
  roles.
- Use HTTPS endpoints.
- Rotate the CA and client credentials if a backup is exposed.
