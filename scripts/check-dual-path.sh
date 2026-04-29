#!/usr/bin/env bash
# Warning when an Endpoint-level change touches only one of the two orchestration paths.
# Warning only — does not block the commit.
# See contributing/architecture.md#cluster-modes

CHANGED=$(git diff --cached --name-only 2>/dev/null || true)
[ -z "$CHANGED" ] && exit 0

K8S_TOUCHED=$(echo "$CHANGED" | grep -cE '/(kubernetes_orchestrator(_resource)?|kubernetes_cluster)\.go$' || true)
RAY_TOUCHED=$(echo "$CHANGED" | grep -cE '/(ray_orchestrator|ray_ssh_cluster|ray_ssh_operation)\.go$' || true)

if [ "$K8S_TOUCHED" -gt 0 ] && [ "$RAY_TOUCHED" -eq 0 ]; then
  echo "⚠️  Kubernetes orchestration changed; SSH/Ray did not."
  echo "   ✅ FIX: review internal/orchestrator/ray_orchestrator.go and internal/cluster/ray_ssh_*.go for parallel changes."
  echo "   📖 See: contributing/architecture.md#cluster-modes"
  echo "   If the change truly applies to only one path, explain why in the PR body."
  echo
fi

if [ "$RAY_TOUCHED" -gt 0 ] && [ "$K8S_TOUCHED" -eq 0 ]; then
  echo "⚠️  SSH/Ray orchestration changed; Kubernetes did not."
  echo "   ✅ FIX: review internal/orchestrator/kubernetes_orchestrator*.go and internal/cluster/kubernetes_cluster.go for parallel changes."
  echo "   📖 See: contributing/architecture.md#cluster-modes"
  echo "   If the change truly applies to only one path, explain why in the PR body."
  echo
fi

# Warning only — always exit success.
exit 0
