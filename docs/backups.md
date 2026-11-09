# Backup and recovery

kouja creates a recovery bundle on the configured backup interval. The same
bundle can be uploaded to S3.

## Local bundle

The backup manager writes a consistent SQLite snapshot plus the files needed to
restore the agent:

- `state.db` — consistent SQLite snapshot
- `secret.key` — credential encryption key
- `host-id` — host identity
- `revoked-serials.log` — revocation journal snapshot
- `config.yaml` — active configuration file
- `emergency.log` — emergency log, if present
- `tls/server.crt`, `tls/server.key`, `tls/client-ca.crt`
- `ca/ca.key` — certificate-authority signing key
- `certificates/` — issued certificate and key files
- `restore.json` — backup manifest

The archive is `kouja-<timestamp>.tar.gz`. Local retention follows
`state.backup_retention_short`, `_daily`, and `_weekly`.

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
```

Credentials use the standard AWS default chain: environment variables, shared
credentials, instance profile, IRSA, or another configured provider. The agent
needs `s3:PutObject` for the configured bucket/prefix. Bucket lifecycle rules
are the recommended retention mechanism for S3; the agent does not delete old
S3 objects itself.

The S3 object key is:

```text
<prefix>/<archive-name>
```

For example:

```text
kouja/kouja-20261109T160000.000000000Z.tar.gz
```

The local staging directory is `state.backup_target` when set, otherwise
`state.backup_dir`. The bundle is uploaded after it is fully written and synced.

## Restore procedure

1. Configure AWS credentials with `s3:GetObject` for the backup bucket.
2. Download the desired archive.
3. Stop the agent.
4. Verify the archive contents and checksums from `restore.json`.
5. Extract the archive into a staging directory.
6. Restore `state.db`, `secret.key`, `host-id`, `config.yaml`,
   `revoked-serials.log`, TLS files, CA files, and `certificates/` to their
   configured paths.
7. Keep the private files at mode `0600` and the CA/certificate directory at
   `0700`.
8. Start the agent and verify `/readyz`, the admin UI, certificate listing, and
   a test mTLS request.

The database and revocation journal must be restored together. Restoring an
older database with a newer journal is safe for revocation; restoring a newer
database with an older journal loses revocations made after the journal
snapshot. Re-revoke missing credentials with `RevokeCertificate` or
`RevokeExternalCertificate` after restore.

## Security

The bundle contains the CA private key, TLS private keys, the credential
encryption key, and client certificate private keys. Treat the S3 bucket as
highly sensitive:

- Keep the bucket private and block public access.
- Enable default encryption or provide a KMS key.
- Restrict `s3:PutObject`/`s3:GetObject` to the agent and recovery roles.
- Use HTTPS endpoints.
- Rotate the CA and client credentials if a bundle is exposed.
