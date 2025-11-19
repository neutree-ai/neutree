#!/bin/bash
# neutree 自动发布 CI 任务脚本
set -e

# 1. 控制面离线物料打包（Helm/Docker Compose）
echo "[1/3] 打包控制面离线物料..."
CONTROL_OFFLINE_DIR="out/control-offline"
mkdir -p "$CONTROL_OFFLINE_DIR"
# 收集 Helm Chart
tar -czf "$CONTROL_OFFLINE_DIR/neutree-helm-chart.tar.gz" -C deploy/chart neutree
# 收集 Docker Compose 相关文件
tar -czf "$CONTROL_OFFLINE_DIR/neutree-docker-compose.tar.gz" -C deploy/docker .

# 2. SSH 类型集群镜像包打包
echo "[2/3] 打包 SSH 类型集群镜像包..."
CLUSTER_OFFLINE_DIR="out/cluster-offline"
mkdir -p "$CLUSTER_OFFLINE_DIR"
# 构建并导出镜像（示例，需根据实际 Dockerfile 和集群定义完善）
docker build -f cluster-image-builder/Dockerfile -t neutree-ssh-cluster:latest cluster-image-builder/
mkdir -p "$CLUSTER_OFFLINE_DIR/tmp"
# Save image to tar file
docker save -o "$CLUSTER_OFFLINE_DIR/tmp/neutree-ssh-cluster.tar" neutree-ssh-cluster:latest

# create manifest.json describing files inside the package
REPO_TAGS=$(docker inspect --format='{{json .RepoTags}}' neutree-ssh-cluster:latest)
echo "{\"images\":[{\"file\":\"neutree-ssh-cluster.tar\", \"repoTags\":$REPO_TAGS}]}" > "$CLUSTER_OFFLINE_DIR/tmp/manifest.json"

tar -czf "$CLUSTER_OFFLINE_DIR/neutree-ssh-cluster-image.tar.gz" -C "$CLUSTER_OFFLINE_DIR/tmp" .

# 3. engine 离线镜像包打包
echo "[3/3] 打包 engine 离线镜像包..."
ENGINE_OFFLINE_DIR="out/engine-offline"
mkdir -p "$ENGINE_OFFLINE_DIR"
# 调用已有脚本自动拉取并打包（需完善 build-engine-package.sh 支持官方仓库拉取）

# Build engine package. If PULL_FROM_REGISTRY is true, pull images first.
if [ "$PULL_FROM_REGISTRY" = "true" ]; then
	bash scripts/builder/build-engine-package.sh -P "$ENGINE_OFFLINE_DIR"
else
	bash scripts/builder/build-engine-package.sh "$ENGINE_OFFLINE_DIR"
fi

# 4. 上传到指定服务器（示例，需根据实际上传方式完善）
echo "上传所有离线包到指定服务器..."
# 示例：使用 curl 上传到 HTTP API
# If UPLOAD_URL is set, upload created artifacts to the server using curl.
if [ -n "$UPLOAD_URL" ]; then
	echo "Uploading control offline packages..."
	curl -H "Authorization: Bearer $UPLOAD_TOKEN" -F "file=@$CONTROL_OFFLINE_DIR/neutree-helm-chart.tar.gz" "$UPLOAD_URL" || true
	curl -H "Authorization: Bearer $UPLOAD_TOKEN" -F "file=@$CONTROL_OFFLINE_DIR/neutree-docker-compose.tar.gz" "$UPLOAD_URL" || true

	echo "Uploading cluster offline package..."
	curl -H "Authorization: Bearer $UPLOAD_TOKEN" -F "file=@$CLUSTER_OFFLINE_DIR/neutree-ssh-cluster-image.tar.gz" "$UPLOAD_URL" || true

	echo "Uploading engine offline package..."
	curl -H "Authorization: Bearer $UPLOAD_TOKEN" -F "file=@$ENGINE_OFFLINE_DIR/engine-package.tar.gz" "$UPLOAD_URL" || true
fi

echo "所有物料打包并上传完成。"
