import unittest
from types import SimpleNamespace

from router.service_discovery import K8sPodSnapshot, pod_snapshot_from_k8s_pod


class RouterServiceDiscoveryTests(unittest.TestCase):
    def test_pod_snapshot_keeps_ready_inference_pod_metadata(self):
        pod = SimpleNamespace(
            metadata=SimpleNamespace(
                name="ep-0",
                deletion_timestamp=None,
                labels={"workspace": "ws", "endpoint": "ep", "routing_logic": "consistent_hash"},
            ),
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
            ),
        )

    def test_pod_snapshot_marks_terminating_pod_not_ready(self):
        pod = SimpleNamespace(
            metadata=SimpleNamespace(name="ep-0", deletion_timestamp="now", labels={}),
            status=SimpleNamespace(
                pod_ip="10.0.0.1",
                container_statuses=[SimpleNamespace(ready=True)],
            ),
        )

        snapshot = pod_snapshot_from_k8s_pod(pod)

        self.assertFalse(snapshot.ready)


if __name__ == "__main__":
    unittest.main()
