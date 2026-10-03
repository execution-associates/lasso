# Execution Associates production

- The hosted Lasso service is a release binary in the unprivileged `lasso-workspace` Incus container on HostHatch VPS `85.155.179.33`. Its public hostname is `https://lasso.anyaiyouwant.com`, behind Cloudflare Access.
- The checkout at `/home/dev/projects/lasso` is source only. A push to `main` runs CI; a version tag publishes a GitHub Release. Neither event updates the hosted binary.
- Use [the production deployment guide](docs/production-deployment.md) for a supervised binary upgrade, guest systemd restart, rollback, and verification. Do not deploy this service to the retired `170.205.38.181` VPS or into an application K3s namespace.
- Keep host root, Incus administration, cluster credentials, and the production deployment key outside the Lasso workspace. Application releases from Lasso go through `execution-associates/hosthatch-ops/new-server/release-production.py`.
