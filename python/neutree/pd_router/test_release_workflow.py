from pathlib import Path
import unittest


class PDRouterReleaseWorkflowTests(unittest.TestCase):
    def test_release_workflow_builds_pushes_and_publishes_manifest(self):
        workflow = Path(".github/workflows/release-pd-router.yaml")

        self.assertTrue(workflow.exists(), "pd-router release workflow is missing")

        text = workflow.read_text()
        for expected in (
            "name: release-pd-router",
            "docker-build-pd-router",
            "docker-push-pd-router",
            "docker-push-manifest-pd-router",
            "SERVE_IMAGE_REPO",
            "RELEASE_SERVE_IMAGE_PROJECT",
        ):
            self.assertIn(expected, text)


if __name__ == "__main__":
    unittest.main()
