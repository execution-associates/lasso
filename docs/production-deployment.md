# Hosted Lasso deployment

This document describes Execution Associates' hosted instance, not every Lasso installation. It runs as the `dev` user inside the unprivileged `lasso-workspace` Incus container on HostHatch VPS `85.155.179.33`. Guest systemd unit `lasso-workspace-lasso.service` serves TCP 8090, after `lasso-workspace-herdr.service`. The host `lasso-workspace-proxy.socket` forwards loopback TCP 8090 to the guest. A separate Cloudflare tunnel and Access application expose `https://lasso.anyaiyouwant.com`.

The checkout in `/home/dev/projects/lasso` does not serve production code. `.github/workflows/ci.yml` checks pushes to `main`; `.github/workflows/release.yml` publishes a binary on a version tag. **A green CI run or published release is not a hosted deployment.** The installed binary is `/home/dev/.local/bin/lasso`; systemd starts that path with `-require-access-header` and `-disable-self-update`.

## Supervised upgrade

Use a maintainer session on the new VPS. First confirm the intended GitHub Release tag, CI run, binary checksum, and a fresh workspace/foundation backup. Record the current `lasso --version` output and `sha256sum /home/dev/.local/bin/lasso`. Preserve the current binary outside the install path for rollback.

Run `lasso update --no-restart` inside the guest as `dev`. Check that the new binary's version and checksum match the intended GitHub Release asset. Then restart only `lasso-workspace-lasso.service` inside the guest. Verify both guest services and the host proxy are active; compare the running process executable checksum with the installed binary; check local HTTP with the required Access email header; open `https://lasso.anyaiyouwant.com` through Cloudflare Access on a computer or phone. Keep the previous binary until those checks pass. The [HostHatch operations guide](https://github.com/execution-associates/hosthatch-ops/blob/main/new-server/LASSO-WORKSPACE.md) records the exact service paths, backups, network isolation, and rollback.

This control-plane service intentionally lives outside K3s so the workspace stays available during app-cluster faults. It is a separate supervised release path from the 20 K3s application image components. Public Lasso does not currently expose a source-commit revision endpoint, so a general app build must never claim to have deployed Lasso; verify its running binary checksum and GitHub Release tag explicitly.
