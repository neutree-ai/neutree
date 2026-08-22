# Structured component volumes across runtime backends

Status: accepted (amended 2026-08-19)

Amendment: the pinned NPU Exporter v26.1.0 fixture requires a working runtime
parser for container/process attribution. This replaces the original
socket-free Profile assumption with explicit, backend-specific runtime mounts;
allocation ownership remains independent scheduler evidence.

Neutree-managed and plugin-owned component mounts will use a backend-neutral
`ComponentVolume` and `ComponentVolumeMount` model, transformed into
Kubernetes volumes or Docker mounts by backend-specific transformers. The
public model will not expose `corev1.Volume`, because many Kubernetes volume
sources have no Docker equivalent. Profile-defined `--volume`, `-v`, `--mount`,
and `--device` flags are disallowed in `DockerRunOptions`; that field remains a
temporary compatibility path only for non-mount Docker flags. This makes NPU
Exporter host driver, device, and socket access explicit and validates it
consistently on Kubernetes and static Ray/SSH. Initial `ComponentVolume`
support is limited to typed host paths; ConfigMap files and ModelCache NFS/PVC
storage keep their dedicated models. The existing static-node
`NodeComponentVolume` is not migrated in the initial NPU delivery, keeping the
change scoped to accelerator exporter profiles and their transformers.

Exporter runtime privilege is equally explicit: the profile gains a
backend-neutral `Privileged` boolean that defaults to false and maps to the
native Kubernetes or Docker setting only when explicitly true. It cannot be
hidden in `DockerRunOptions`; an Enterprise NPU profile may enable it only when
vendor requirements and hardware validation document the reason. The initial
NPU profile may explicitly use `true`: the official exporter deployment
requires host device access and the validated 310P probe used this setting.
Its image digest, mounts, runtime endpoint variant, and justification are recorded;
later least-privilege work does not block the initial delivery.

The Kubernetes + containerd NPU exporter Profile mounts `/run/containerd`
read-only and sets `-containerMode=containerd`. The pinned v26.1.0 evidence shows
that exporter container/process series require a working runtime parser. This
runtime evidence does not become allocation truth: PodResources, per-Pod HAMi
annotations, and Ray Actor/process joins still establish allocation ownership,
while exporter process/container records only prove device usage attribution.

Static Docker is a separate runtime variant. It sets `-containerMode=docker`
only after the target Docker and exporter versions establish the exact endpoint,
minimal read-only mount, readiness, and restart behavior in E2E. A containerd
path cannot be reused as its contract. Each backend Profile therefore declares
its own runtime argument and structured socket/directory mount. These Exporter
mounts do not propagate to NodeAgent; the Adapter's driver/DCMI runtime remains
an independently validated component requirement.

Exporter networking is backend-selected. The initial NPU exporter uses host
networking on static Docker nodes so the local Node-agent scrapes localhost.
Kubernetes managed exporters use Pod networking and local Pod-IP discovery;
they do not project the static `HostNetwork` requirement. Shared exporter args
must carry the required wildcard `-ip=0.0.0.0` and must not bind only to
loopback.

The exporter launch model must also preserve OCI command semantics. An exporter
profile has explicit `Command` and `Args` fields, transformed into Kubernetes
`container.command` and `container.args`, respectively. This is required by
the verified Ascend exporter image, which has no `ENTRYPOINT` and encodes its
binary only in `CMD`; setting Kubernetes args alone would replace that command
and attempt to execute the first flag. The initial NPU Profile sets
`Command: ["/usr/local/bin/npu-exporter"]` and retains the shared arguments.
