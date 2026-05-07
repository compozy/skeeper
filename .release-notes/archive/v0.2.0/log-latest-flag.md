---
title: skeeper log --latest
type: feature
---

`skeeper log <path>` reads the locked sidecar commit by default, matching `skeeper hydrate`. The new `--latest` flag fetches the namespace branch and reads its current tip instead, which is useful when investigating why the working tree disagrees with the lockfile or when reviewing in-flight changes from another contributor before they land in `main`.
