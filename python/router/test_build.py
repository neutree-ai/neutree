from pathlib import Path
import unittest


class RouterBuildTests(unittest.TestCase):
    def test_router_image_build_entrypoints_exist(self):
        dockerfile = Path("Dockerfile.router")
        makefile = Path("Makefile")

        self.assertTrue(dockerfile.exists(), "Dockerfile.router is missing")
        dockerfile_text = dockerfile.read_text()
        self.assertIn("python/router/requirements.txt", dockerfile_text)
        self.assertIn('ENTRYPOINT ["python", "-m", "router"]', dockerfile_text)

        makefile_text = makefile.read_text()
        self.assertIn("docker-build-router", makefile_text)
        self.assertIn("docker-push-router", makefile_text)
        self.assertIn("docker-push-manifest-router", makefile_text)


if __name__ == "__main__":
    unittest.main()
