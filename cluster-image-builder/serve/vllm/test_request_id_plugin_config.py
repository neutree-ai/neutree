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
        keyword.arg == "validate"
        and isinstance(keyword.value, ast.Constant)
        and keyword.value.value is False
    )


def _request_id_plugin_disables_validation(call):
    return (
        isinstance(call, ast.Call)
        and _is_name(call.func, "RequestIdPlugin")
        and any(_is_false_keyword(keyword) for keyword in call.keywords)
    )


def _plugins_disable_request_id_validation(node):
    for keyword in node.keywords:
        if keyword.arg != "plugins":
            continue

        plugins = keyword.value
        if isinstance(plugins, (ast.Tuple, ast.List)):
            return any(
                _request_id_plugin_disables_validation(element)
                for element in plugins.elts
            )

        return _request_id_plugin_disables_validation(plugins)

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


class TestVLLMRequestIdPluginConfig(unittest.TestCase):
    def test_vllm_apps_accept_opaque_client_request_ids(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in VLLM_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())
                middleware_calls = [
                    node
                    for node in ast.walk(tree)
                    if _is_raw_context_middleware_registration(node)
                ]

                self.assertEqual(
                    len(middleware_calls),
                    1,
                    f"{app_file} should register RawContextMiddleware exactly once",
                )
                self.assertTrue(
                    _plugins_disable_request_id_validation(middleware_calls[0]),
                    f"{app_file} should configure RequestIdPlugin(validate=False)",
                )


if __name__ == "__main__":
    unittest.main()
