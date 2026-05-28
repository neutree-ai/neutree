from __future__ import annotations

import json
import re
import threading
import time
import urllib.request
import uuid
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Set

from router.routing import EndpointInfo, PDRouteUnit


PD_COMPONENT_LABEL = "pd-collocated"


@dataclass(frozen=True)
class K8sContainerSnapshot:
    name: str
    ready: bool
    http_port: Optional[int] = None


@dataclass(frozen=True)
class K8sPodSnapshot:
    name: str
    ip: Optional[str]
    ready: bool
    workspace: Optional[str]
    endpoint: Optional[str]
    routing_logic: Optional[str]
    uid: str = ""
    is_pd_collocated: bool = False
    containers: List[K8sContainerSnapshot] = field(default_factory=list)


def pod_snapshot_from_k8s_pod(pod: object) -> K8sPodSnapshot:
    metadata = getattr(pod, "metadata", None)
    status = getattr(pod, "status", None)
    labels = getattr(metadata, "labels", None) or {}
    container_statuses = getattr(status, "container_statuses", None) or []
    ready_by_name = {
        getattr(container_status, "name", ""): bool(getattr(container_status, "ready", False))
        for container_status in container_statuses
        if getattr(container_status, "name", "")
    }
    containers = _container_snapshots(getattr(getattr(pod, "spec", None), "containers", None) or [], ready_by_name)
    ready = bool(container_statuses) and all(
        bool(getattr(container_status, "ready", False))
        for container_status in container_statuses
    )
    ready = ready and getattr(metadata, "deletion_timestamp", None) is None
    return K8sPodSnapshot(
        name=getattr(metadata, "name", ""),
        ip=getattr(status, "pod_ip", None),
        ready=ready,
        workspace=labels.get("workspace"),
        endpoint=labels.get("endpoint"),
        routing_logic=labels.get("routing_logic"),
        uid=getattr(metadata, "uid", "") or getattr(metadata, "name", ""),
        is_pd_collocated=labels.get("neutree.io/component") == PD_COMPONENT_LABEL,
        containers=containers,
    )


def _container_snapshots(containers: List[object], ready_by_name: Dict[str, bool]) -> List[K8sContainerSnapshot]:
    result = []
    for container in containers:
        name = getattr(container, "name", "")
        http_port = None
        for port in getattr(container, "ports", None) or []:
            if getattr(port, "name", None) == "http":
                http_port = int(getattr(port, "container_port"))
                break
        result.append(
            K8sContainerSnapshot(
                name=name,
                ready=ready_by_name.get(name, False),
                http_port=http_port,
            )
        )
    return result


class K8sPodServiceDiscovery:
    def __init__(
        self,
        namespace: str,
        port: int,
        label_selector: str,
        watcher_timeout_seconds: int = 0,
        health_check_timeout_seconds: int = 10,
    ):
        self.namespace = namespace
        self.port = port
        self.label_selector = label_selector
        self.watcher_timeout_seconds = watcher_timeout_seconds
        self.health_check_timeout_seconds = health_check_timeout_seconds
        self._available: Dict[str, EndpointInfo] = {}
        self._known_models: Set[str] = set()
        self._lock = threading.Lock()
        self._running = True

        from kubernetes import client, config, watch

        try:
            config.load_incluster_config()
        except config.ConfigException:
            config.load_kube_config()

        self._api = client.CoreV1Api()
        self._watch = watch.Watch()
        self._thread = threading.Thread(target=self._watch_pods, daemon=True)
        self._thread.start()

    def get_endpoint_info(self) -> List[EndpointInfo]:
        with self._lock:
            return list(self._available.values())

    def has_ever_seen_model(self, model_name: str) -> bool:
        with self._lock:
            return model_name in self._known_models

    def get_health(self) -> bool:
        return self._thread.is_alive()

    def close(self) -> None:
        self._running = False
        self._watch.stop()
        self._thread.join(timeout=5)

    def _watch_pods(self) -> None:
        while self._running:
            try:
                for event in self._watch.stream(
                    self._api.list_namespaced_pod,
                    namespace=self.namespace,
                    label_selector=self.label_selector,
                    timeout_seconds=self.watcher_timeout_seconds,
                ):
                    pod = event["object"]
                    event_type = event["type"]
                    snapshot = pod_snapshot_from_k8s_pod(pod)
                    if event_type == "DELETED" or not snapshot.ready:
                        self._remove(snapshot.name)
                    elif snapshot.ip:
                        self._add_or_update(snapshot)
            except Exception:
                time.sleep(0.5)

    def _add_or_update(self, snapshot: K8sPodSnapshot) -> None:
        if snapshot.is_pd_collocated:
            endpoint_info = self._build_pd_endpoint(snapshot)
            if endpoint_info is None:
                self._remove(snapshot.name)
                return
            with self._lock:
                self._available[snapshot.name] = endpoint_info
                self._known_models.update(endpoint_info.model_names)
            return

        model_names = self._get_model_names(snapshot.ip, self.port)
        if not model_names:
            self._remove(snapshot.name)
            return
        endpoint_info = EndpointInfo(
            url=f"http://{snapshot.ip}:{self.port}",
            model_names=model_names,
            id=str(uuid.uuid5(uuid.NAMESPACE_DNS, snapshot.name)),
            workspace=snapshot.workspace,
            endpoint=snapshot.endpoint,
            routing_logic=snapshot.routing_logic,
            pod_name=snapshot.name,
        )
        with self._lock:
            self._available[snapshot.name] = endpoint_info
            self._known_models.update(model_names)

    def _remove(self, pod_name: str) -> None:
        with self._lock:
            self._available.pop(pod_name, None)

    def _build_pd_endpoint(self, snapshot: K8sPodSnapshot) -> Optional[EndpointInfo]:
        sidecar = _find_container(snapshot.containers, "pd-router-sidecar")
        if sidecar is None or not sidecar.ready or not sidecar.http_port:
            return None

        model_port = _first_ready_role_http_port(snapshot.containers)
        model_names = self._get_model_names(snapshot.ip, model_port)
        if not model_names:
            return None

        sidecar_url = f"http://{snapshot.ip}:{sidecar.http_port}"
        route_units = _pd_route_units(snapshot, sidecar_url)
        if not route_units:
            return None

        return EndpointInfo(
            url=sidecar_url,
            model_names=model_names,
            id=str(uuid.uuid5(uuid.NAMESPACE_DNS, snapshot.name)),
            workspace=snapshot.workspace,
            endpoint=snapshot.endpoint,
            routing_logic=snapshot.routing_logic,
            pod_name=snapshot.name,
            is_pd_collocated=True,
            pd_route_units=route_units,
        )

    def _get_model_names(self, pod_ip: Optional[str], port: Optional[int]) -> List[str]:
        if not port:
            return []
        if not pod_ip:
            return []
        url = f"http://{pod_ip}:{port}/v1/models"
        try:
            with urllib.request.urlopen(url, timeout=self.health_check_timeout_seconds) as response:
                body = response.read()
            payload = json.loads(body.decode("utf-8"))
            return [
                model.get("id")
                for model in payload.get("data", [])
                if isinstance(model, dict) and model.get("id")
            ]
        except Exception:
            return []


def _find_container(containers: List[K8sContainerSnapshot], name: str) -> Optional[K8sContainerSnapshot]:
    for container in containers:
        if container.name == name:
            return container
    return None


def _first_ready_role_http_port(containers: List[K8sContainerSnapshot]) -> Optional[int]:
    for container in containers:
        if container.ready and container.http_port and _role_and_index(container.name)[0] in {"prefill", "decode"}:
            return container.http_port
    return None


def _pd_route_units(snapshot: K8sPodSnapshot, sidecar_url: str) -> List[PDRouteUnit]:
    units = []
    role_group_id = snapshot.uid or snapshot.name
    for container in snapshot.containers:
        role, index = _role_and_index(container.name)
        if role not in {"prefill", "decode"} or index is None:
            continue
        units.append(
            PDRouteUnit(
                role_group_id=role_group_id,
                role=role,
                index=index,
                ready=container.ready and container.http_port is not None,
                sidecar_url=sidecar_url,
            )
        )
    return units


def _role_and_index(container_name: str) -> tuple[Optional[str], Optional[int]]:
    match = re.match(r"^(prefill|decode)-([0-9]+)$", container_name)
    if not match:
        return None, None
    return match.group(1), int(match.group(2))
