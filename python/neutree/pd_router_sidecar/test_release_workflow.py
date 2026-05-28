from pathlib import Path
import unittest


class PDRouterSidecarReleaseWorkflowTests(unittest.TestCase):
    def test_release_workflow_builds_pushes_and_publishes_manifest(self):
        workflow = Path(".github/workflows/release-pd-router-sidecar.yaml")

        self.assertTrue(workflow.exists(), "pd-router-sidecar release workflow is missing")

        text = workflow.read_text()
        for expected in (
            "name: release-pd-router-sidecar",
            "docker-build-pd-router-sidecar",
            "docker-push-pd-router-sidecar",
            "docker-push-manifest-pd-router-sidecar",
            "SERVE_IMAGE_REPO",
            "RELEASE_SERVE_IMAGE_PROJECT",
        ):
            self.assertIn(expected, text)


if __name__ == "__main__":
    unittest.main()
