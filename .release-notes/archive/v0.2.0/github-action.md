---
title: Official compozy/skeeper GitHub Action
type: feature
---

The repo now ships a same-repository GitHub Action (`action.yml`) that downloads the released Skeeper binary for the requested version and delegates to the CLI. Default arguments run `skeeper verify` so pull requests fail when `skeeper.lock` and the sidecar remote disagree.

```yaml
- uses: compozy/skeeper@v0.2.0
  with:
    args: |
      verify
      --json
    ssh-private-key: ${{ secrets.SKEEPER_SSH_PRIVATE_KEY }}
```

Credential precedence: `ssh-private-key` writes a temp key and sets `GIT_SSH_COMMAND`; otherwise `token` configures HTTPS GitHub credentials; otherwise the runner's existing Git/SSH config is used. Secrets are masked before configuration, the temp key is wiped on `always()` cleanup, and Linux/macOS/Windows on amd64/arm64 are all resolved through the same release manifest.
