import unittest
from types import SimpleNamespace

from router.service_discovery import (
    K8sContainerSnapshot,
    K8sPodServiceDiscovery,
    K8sPodSnapshot,
    pod_snapshot_from_k8s_pod,
)


class RouterServiceDiscoveryTests(unittest.TestCase):
    def test_pod_snapshot_keeps_ready_inference_pod_metadata(self):
        pod = SimpleNamespace(
            metadata=SimpleNamespace(
                name="ep-0",
                uid="pod-uid",
                deletion_timestamp=None,
                labels={"workspace": "ws", "endpoint": "ep", "routing_logic": "consistent_hash"},
            ),
            spec=SimpleNamespace(containers=[]),
            status=SimpleNamespace(
                pod_ip="10.0.0.1",
                container_statuses=[
                    SimpleNamespace(ready=True),
                    SimpleNamespace(ready=True),
                ],
            ),
        )

        snapshot = pod_snapshot_from_k8s_pod(pod)

        self.assertEqual(
            snapshot,
            K8sPodSnapshot(
                name="ep-0",
                ip="10.0.0.1",
                ready=True,
                workspace="ws",
                endpoint="ep",
                routing_logic="consistent_hash",
                uid="pod-uid",
            ),
        )

    def test_pod_snapshot_marks_terminating_pod_not_ready(self):
        pod = SimpleNamespace(
            metadata=SimpleNamespace(name="ep-0", uid="pod-uid", deletion_timestamp="now", labels={}),
            spec=SimpleNamespace(containers=[]),
            status=SimpleNamespace(
                pod_ip="10.0.0.1",
                container_statuses=[SimpleNamespace(ready=True)],
            ),
        )

        snapshot = pod_snapshot_from_k8s_pod(pod)

        self.assertFalse(snapshot.ready)

    def test_pd_pod_snapshot_extracts_sidecar_and_role_http_ports(self):
        def container(name, ports):
            return SimpleNamespace(
                name=name,
                ports=[
                    SimpleNamespace(name=port_name, container_port=container_port)
                    for port_name, container_port in ports
                ],
            )

        pod = SimpleNamespace(
            metadata=SimpleNamespace(
                name="ep-pd-0",
                uid="pod-uid",
                deletion_timestamp=None,
                labels={
                    "workspace": "ws",
                    "endpoint": "ep",
                    "routing_logic": "consistent_hash",
                    "neutree.io/component": "pd-collocated",
                },
            ),
            spec=SimpleNamespace(
                containers=[
                    container("pd-router-sidecar", [("http", 8000)]),
                    container("prefill-0", [("http", 8100), ("side-channel", 8101)]),
                    container("decode-0", [("http", 8200)]),
                ],
            ),
            status=SimpleNamespace(
                pod_ip="10.0.0.1",
                container_statuses=[
                    SimpleNamespace(name="pd-router-sidecar", ready=True),
                    SimpleNamespace(name="prefill-0", ready=True),
                    SimpleNamespace(name="decode-0", ready=True),
                ],
            ),
        )

        snapshot = pod_snapshot_from_k8s_pod(pod)

        self.assertTrue(snapshot.is_pd_collocated)
        self.assertEqual(
            snapshot.containers,
            [
                K8sContainerSnapshot(name="pd-router-sidecar", ready=True, http_port=8000),
                K8sContainerSnapshot(name="prefill-0", ready=True, http_port=8100),
                K8sContainerSnapshot(name="decode-0", ready=True, http_port=8200),
            ],
        )

    def test_pd_pod_expands_role_containers_into_endpoint_infos(self):
        discovery = object.__new__(K8sPodServiceDiscovery)
        discovery._get_model_names = (
            lambda pod_ip, port: ["m"] if (pod_ip, port) == ("10.0.0.1", 8100) else []
        )
        snapshot = K8sPodSnapshot(
            name="ep-pd-0",
            ip="10.0.0.1",
            ready=True,
            workspace="ws",
            endpoint="ep",
            routing_logic="consistent_hash",
            uid="pod-uid",
            is_pd_collocated=True,
            containers=[
                K8sContainerSnapshot(name="pd-router-sidecar", ready=True, http_port=8000),
                K8sContainerSnapshot(name="prefill-0", ready=True, http_port=8100),
                K8sContainerSnapshot(name="decode-1", ready=True, http_port=8201),
            ],
        )

        endpoints = discovery._build_pd_endpoints(snapshot)

        self.assertEqual(len(endpoints), 2)
        by_role = {endpoint.pd_role: endpoint for endpoint in endpoints}
        self.assertEqual(by_role["prefill"].route_key, "pod-uid:prefill:0")
        self.assertEqual(by_role["decode"].route_key, "pod-uid:decode:1")
        for endpoint in endpoints:
            self.assertEqual(endpoint.dispatch_url, "http://10.0.0.1:8000")
            self.assertEqual(endpoint.model_names, ["m"])
            self.assertTrue(endpoint.is_pd_collocated)
            self.assertEqual(endpoint.domain, "pod-uid")
            self.assertEqual(endpoint.scheduling_domain, "pod-uid")
            self.assertEqual(endpoint.pd_role_group_id, "pod-uid")
            self.assertFalse(endpoint.sleep)


if __name__ == "__main__":
    unittest.main()
