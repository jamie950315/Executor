# Executor Deployment

## Cloudflare Tunnel

Executor expects a remotely-managed Cloudflare Named Tunnel and a proxied CNAME record that points to `<tunnel-id>.cfargotunnel.com`.

- Create the tunnel with the Cloudflare Tunnel API.
- Apply remote ingress rules with a catch-all `http_status:404`.
- Point the public hostname at the tunnel target with a proxied CNAME.
- Fetch the tunnel token and store it in a mode-600 token file.
- Run `cloudflared tunnel run --token-file <path>` instead of storing a local credentials JSON or inline token.

This matches the current Cloudflare documentation for remotely-managed tunnels, including the tunnel token endpoint and `--token-file` support for `cloudflared` 2025.4.0 or later.

## Service Templates

The service bundle renderer produces:

- macOS LaunchDaemons for `executor agent` and `executor broker`
- macOS LaunchAgent for `executor desktop`, loaded into the real console owner's `gui/<uid>` session rather than `gui/0`
- macOS LaunchDaemon for `cloudflared` using `--token-file`
- Linux systemd units for `agent` and `broker`
- Linux user unit for `desktop`, enabled through the invoking desktop user with `XDG_RUNTIME_DIR`
- Linux systemd unit for `cloudflared` using `--token-file`
- Windows PowerShell scripts for Executor service install, per-user desktop startup, and `cloudflared` registry configuration with `--token-file`; the agent service uses a virtual service account instead of a persisted plaintext password
- WSL guidance that keeps desktop control on the Windows companion

## Rollback

- `scripts/bootstrap.sh` and `scripts/bootstrap.ps1` write a service manifest plus per-file backups under the state directory before replacing managed files or runtime startup entries.
- `scripts/rollback.sh` and `scripts/rollback.ps1` stop managed services, restore backed up files when present, and remove files or services that were created by the current install.
- The shell installer records whether the dedicated Unix service identity was created during the current install and removes it only in that case during rollback.
- `scripts/uninstall.sh` and `scripts/uninstall.ps1` run rollback first, then remove the local deployment state directory.
- The Go Cloudflare client deletes newly created DNS and tunnel resources in reverse order when a deployment step fails after creation, and removes any freshly written token file.

## GitHub Actions

- `.github/workflows/go.yml` runs `go test ./...` on Linux, macOS, and Windows.
- `scripts/build-release-artifacts.sh` cross-builds `executor` for `darwin`, `linux`, and `windows` on `amd64` and `arm64`, packages each install bundle, and writes `SHA256SUMS.txt`.
- `.github/workflows/release.yml` runs the build matrix, uploads per-target artifacts, and publishes consolidated checksums.
