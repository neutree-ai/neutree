import ast
from pathlib import Path
import unittest


APP_PATH = Path(__file__).with_name("app.py")


class TestFastAPIRequestAnnotations(unittest.TestCase):
    def test_class_based_fastapi_routes_do_not_defer_request_annotations(self):
        tree = ast.parse(APP_PATH.read_text())

        has_future_annotations = any(
            isinstance(node, ast.ImportFrom)
            and node.module == "__future__"
            and any(alias.name == "annotations" for alias in node.names)
            for node in tree.body
        )

        request_route_methods = []
        for node in ast.walk(tree):
            if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                continue
            if not node.args.args:
                continue
            if node.args.args[0].arg != "self":
                continue
            for arg in node.args.args[1:]:
                if arg.arg != "request":
                    continue
                if isinstance(arg.annotation, ast.Name) and arg.annotation.id == "Request":
                    request_route_methods.append(node.name)

        self.assertTrue(request_route_methods)
        self.assertFalse(
            has_future_annotations,
            "Ray Serve class-based FastAPI routes must keep request: Request "
            f"annotations eager; affected methods: {request_route_methods}",
        )


if __name__ == "__main__":
    unittest.main()
