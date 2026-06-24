"""Tests for vLLM Ray Serve request id middleware configuration."""

import ast
import pathlib
import unittest


VLLM_APP_FILES = (
    pathlib.Path("v0_8_5/app.py"),
    pathlib.Path("v0_11_2/app.py"),
    pathlib.Path("v0_17_1/app.py"),
)


def _is_name(node, name):
    return isinstance(node, ast.Name) and node.id == name


def _is_false_keyword(keyword):
    return (
        keyword.arg in ("force_new_uuid", "validate")
        and isinstance(keyword.value, ast.Constant)
        and keyword.value.value is False
    )


def _is_true_constant(node):
    return (
        isinstance(node, ast.Constant)
        and node.value is True
    )


def _request_id_plugin_accepts_client_ids(call):
    if not isinstance(call, ast.Call) or not _is_name(call.func, "RequestIdPlugin"):
        return False

    if call.args and _is_true_constant(call.args[0]):
        return False

    force_new_uuid_keywords = [
        keyword for keyword in call.keywords if keyword.arg == "force_new_uuid"
    ]
    if force_new_uuid_keywords and not all(
        _is_false_keyword(keyword) for keyword in force_new_uuid_keywords
    ):
        return False

    validate_keywords = [keyword for keyword in call.keywords if keyword.arg == "validate"]
    return bool(validate_keywords) and all(
        _is_false_keyword(keyword) for keyword in validate_keywords
    )


def _plugins_disable_request_id_validation(node):
    for keyword in node.keywords:
        if keyword.arg != "plugins":
            continue

        plugins = keyword.value
        if isinstance(plugins, (ast.Tuple, ast.List)):
            return any(
                _request_id_plugin_accepts_client_ids(element)
                for element in plugins.elts
            )

        return _request_id_plugin_accepts_client_ids(plugins)

    return False


def _is_raw_context_middleware_registration(node):
    if not isinstance(node, ast.Call):
        return False

    if not (
        isinstance(node.func, ast.Attribute)
        and node.func.attr == "add_middleware"
        and _is_name(node.func.value, "app")
    ):
        return False

    return bool(node.args) and _is_name(node.args[0], "RawContextMiddleware")


def _is_app_fastapi_assignment(node):
    return (
        isinstance(node, ast.Assign)
        and any(_is_name(target, "app") for target in node.targets)
        and isinstance(node.value, ast.Call)
        and _is_name(node.value.func, "FastAPI")
    )


def _is_serve_ingress_app_decorator(node):
    return (
        isinstance(node, ast.Call)
        and isinstance(node.func, ast.Attribute)
        and node.func.attr == "ingress"
        and _is_name(node.func.value, "serve")
        and len(node.args) == 1
        and _is_name(node.args[0], "app")
    )


def _serve_ingress_app_decorator_lineno(class_def):
    for decorator in class_def.decorator_list:
        if _is_serve_ingress_app_decorator(decorator):
            return decorator.lineno

    return None


class TestVLLMRequestIdPluginConfig(unittest.TestCase):
    def test_vllm_ray_serve_ingress_apps_accept_opaque_request_ids(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in VLLM_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())
                fastapi_app_assignments = [
                    node for node in ast.walk(tree) if _is_app_fastapi_assignment(node)
                ]
                middleware_calls = [
                    node
                    for node in ast.walk(tree)
                    if _is_raw_context_middleware_registration(node)
                ]
                ingress_lines = [
                    line
                    for node in ast.walk(tree)
                    if isinstance(node, ast.ClassDef)
                    for line in (_serve_ingress_app_decorator_lineno(node),)
                    if line is not None
                ]

                self.assertEqual(
                    len(fastapi_app_assignments),
                    1,
                    f"{app_file} should define exactly one FastAPI app",
                )
                self.assertEqual(
                    len(middleware_calls),
                    1,
                    f"{app_file} should register RawContextMiddleware exactly once",
                )
                self.assertEqual(
                    len(ingress_lines),
                    1,
                    f"{app_file} should expose the FastAPI app through serve.ingress(app)",
                )
                self.assertLess(
                    middleware_calls[0].lineno,
                    ingress_lines[0],
                    f"{app_file} should configure request-id middleware before serve.ingress(app)",
                )
                self.assertTrue(
                    _plugins_disable_request_id_validation(middleware_calls[0]),
                    f"{app_file} should configure RequestIdPlugin(validate=False)",
                )


if __name__ == "__main__":
    unittest.main()
