# 离线物料打包与 CI 自动发布

本页记录如何使用仓库内的脚本自动打包控制面、集群镜像和引擎离线包，并将它们上传到指定服务器。

打包脚本位于 `scripts/ci-release.sh`，具体逻辑：

- 控制面（Control）: 打包 Helm Chart（deploy/chart）为 `neutree-helm-chart.tar.gz`，并将 `deploy/docker` 文件打包为 `neutree-docker-compose.tar.gz`。
- SSH 类型的集群镜像（Cluster）: 使用 `cluster-image-builder/Dockerfile` 构建镜像并导出为 `neutree-ssh-cluster-image.tar.gz`。
- Engine 离线包（Engine）: 使用 `scripts/builder/build-engine-package.sh` 来构建 engine 离线包。

上传：

1. CI workflow: `.github/workflows/release-offline.yml` 将在手动触发时打包并将产物上传为 GitHub workflow artifact。可通过 secrets 配置 `UPLOAD_URL` 与 `UPLOAD_TOKEN` 来调用 HTTP 上传到指定服务器。
2. `scripts/ci-release.sh` 也将尝试在环境变量 `UPLOAD_URL` 存在时借助 `curl` 上传。

CLI 扩展：

- `neutree-cli cluster import --offline-image <file> --registry registry.example.com`：将 SSH 类型集群镜像 tarball 导入到指定的 registry 并推送。

- 新：`neutree-cli cluster import` 使用 Go registry SDK（`google/go-containerregistry`）直接从 tarball 读取图像并推送到目标 registry，不再依赖本机 Docker `load` 和 `push`。这在没有 Docker daemon 或 CI 环境中更稳健。
- `neutree-cli launch --offline-package <file>`：支持通过离线包安装控制面组件（支持 docker compose 离线包和 helm chart 离线包），脚本会把包解压到 `NEUTREE_LAUNCH_WORK_DIR` 或工作目录下，然后执行安装。

- 新增全局镜像 registry 支持（仅在 Helm 模式下有效）：可以通过 `neutree-cli launch --deploy-method helm --mirror-registry registry.example.com` 或在 Helm 参数中使用 `--set global.imageRegistry=registry.example.com`，将统一为 chart 中的镜像添加 registry 前缀，方便离线或私有镜像部署。

- 使用 Helm SDK：`neutree-cli` 现在始终在 `--deploy-method helm` 下使用 Helm Go SDK (`helm.sh/helm/v3`)；不再依赖或回退到系统 `helm` 二进制。

注意（Breaking Change）：此版本为首次默认使用 Helm Go SDK 并移除了对 Helm CLI 的回退支持。如果你的环境依赖 `helm` 二进制的行为或自定义插件，请调整为使用在线或脱机 Helm Chart 的方式来适配 SDK。

示例：

	# 使用 docker compose 离线包安装控制面（默认）
	neutree-cli launch neutree-core --offline-package out/control-offline/neutree-docker-compose.tar.gz

	# 使用 Helm chart 离线包安装控制面（需要 Helm 与 Kubernetes 集群）
	neutree-cli launch neutree-core --deploy-method helm --offline-package out/control-offline/neutree-helm-chart.tar.gz

Engine 脚本改进：

- `scripts/builder/build-engine-package.sh` 新增对 `PULL_FROM_REGISTRY` 的支持（若设置为 `true`，在打包前会尝试 `docker pull` 指定镜像，从官方仓库拉取镜像后再进行导出和打包）。

如何触发 CI：

1. 打开 Actions -> release-offline，手动输入 `tag` 和 `version` 后执行。
2. 配置必要的 secrets（UPLOAD_URL、UPLOAD_TOKEN、PULL_FROM_REGISTRY）以启用上传和从仓库拉取功能。
