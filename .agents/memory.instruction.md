---
applyTo: '**'
---

# Coding Preferences
- CLI pattern: Cobra commands are in `cmd/neutree-cli/app/cmd`, prefer adding flags consistent with `commonOptions` and `command.Executor` testing with mocks.
- Packaging: Offline packaging scripts live in `scripts/` and `cluster-image-builder/`; use tar/gz and `docker save` for image bundles.

# Project Architecture
- Helm charts live in `deploy/chart/neutree`. Use `values.yaml` and `_helpers.tpl` to centralize image naming.
- Add `global.imageRegistry` to support air-gapped/registry-mirror installs. Use helper `neutree.image` to apply the registry prefix to image names.
- Add `global.imageRegistry` to support air-gapped/registry-mirror installs. Use helper `neutree.image` to apply the registry prefix to image names.
- Registry SDK: `neutree-cli cluster import` now uses `google/go-containerregistry` to push images from tarballs; this removes dependency on local docker daemon for import.
 - Helm SDK support: `pkg/helmclient` now uses Helm Go SDK (`helm.sh/helm/v3`) to perform chart install/upgrade operations. The previous Helm CLI fallback has been removed and Helm SDK is the only supported path.

# Solutions Repository
- Solution pattern for offline packaging: `scripts/ci-release.sh` builds control, cluster, and engine artifacts and optionally uploads via `UPLOAD_URL`/`UPLOAD_TOKEN`.
- `neutree-cli` offers `--offline-package` and `--deploy-method` to handle offline installs. `--mirror-registry` injects `--set global.imageRegistry` in Helm installs.
