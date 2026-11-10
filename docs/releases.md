# Releases

The release workflow builds static Linux x86_64/amd64 binaries for every
command under `cmd/`, packages them, and publishes the archive and checksums
as a GitHub release.

## Create a release

1. Start from an up-to-date `main` with a clean working tree.
2. Run the checks locally:

   ```sh
   make test
   make lint
   ```

3. Create and push an annotated tag:

   ```sh
   git tag -a v0.1.0 -m "Release v0.1.0"
   git push origin v0.1.0
   ```

4. The `Release` workflow runs on the tag, builds the archive, uploads it to
   the tag's GitHub release, and generates release notes. Edit the release notes
   if additional operational guidance is needed.

The tag must start with `v` (`v*`). The provider release asset omits the `v`
prefix, for example `terraform-provider-kouja_0.1.0_linux_amd64`.

## Release archive

```text
kouja-v0.1.0-linux-amd64.tar.gz
kouja-v0.1.0-linux-amd64.tar.gz.sha256
kouja-v0.1.0-linux-amd64/
  kouja-agent
  kouja-backup
  kouja-csi-controller
  kouja-csi-node
  kouja-restore
  terraform-provider-kouja_0.1.0_linux_amd64
  terraform-provider-kouja_0.1.0_linux_amd64.sha256
  SHA256SUMS
  config.example.yaml
  kouja-init.sh
  deploy/install/install.sh
  deploy/systemd/
  docs/
  README.md
  LICENSE
```

All Go binaries are built with `CGO_ENABLED=0`, `GOOS=linux`,
`GOARCH=amd64`, `-trimpath`, and stripped with `-s -w`. The workflow verifies
that each binary is statically linked and checks all SHA-256 checksums before
publishing.

## Manual release build

Build the same archive locally without publishing:

```sh
make release VERSION=v0.1.0
```

The output is under `dist/`. If a release already exists but lacks assets, run
**Actions → Release → Run workflow** with the existing tag, or upload the
generated files manually with `gh release upload`.

Install from an unpacked archive by running the installer from the archive
root so its relative `deploy/systemd` paths resolve:

```sh
cd kouja-v0.1.0-linux-amd64
sudo ./deploy/install/install.sh ./kouja-agent
```
