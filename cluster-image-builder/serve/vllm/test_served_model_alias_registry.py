"""Static guard for vLLM OpenAI model alias registry wiring."""

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


def _is_self_model_path(node):
    return (
        isinstance(node, ast.Attribute)
        and node.attr == "model_path"
        and _is_name(node.value, "self")
    )


def _is_self_served_model_names(node):
    return (
        isinstance(node, ast.Attribute)
        and node.attr == "served_model_names"
        and _is_name(node.value, "self")
    )


def _imports_build_base_model_paths(tree):
    for node in ast.walk(tree):
        if not isinstance(node, ast.ImportFrom) or node.module != "serve._utils":
            continue
        if any(alias.name == "build_base_model_paths" for alias in node.names):
            return True
    return False


def _calls_build_base_model_paths_with_actual_model_path(call):
    if not isinstance(call, ast.Call) or not _is_name(call.func, "build_base_model_paths"):
        return False
    if not call.args or not _is_name(call.args[0], "BaseModelPath"):
        return False
    return any(_is_self_model_path(arg) for arg in call.args) or any(
        keyword.arg == "model_path" and _is_self_model_path(keyword.value)
        for keyword in call.keywords
    )


def _calls_build_base_model_paths_with_served_model_names(call):
    if not isinstance(call, ast.Call) or not _is_name(call.func, "build_base_model_paths"):
        return False
    return len(call.args) >= 2 and _is_self_served_model_names(call.args[1])


def _assigns_served_model_names_from_effective_args(node):
    if not isinstance(node, ast.Assign):
        return False
    if not any(_is_self_served_model_names(target) for target in node.targets):
        return False
    value = node.value
    return (
        isinstance(value, ast.Call)
        and isinstance(value.func, ast.Attribute)
        and value.func.attr == "get"
        and _is_name(value.func.value, "args")
        and value.args
        and isinstance(value.args[0], ast.Constant)
        and value.args[0].value == "served_model_name"
    )


def _calls_base_model_path_directly(node):
    return isinstance(node, ast.Call) and _is_name(node.func, "BaseModelPath")


def _is_model_config_served_model_name(node):
    return (
        isinstance(node, ast.Attribute)
        and node.attr == "served_model_name"
        and isinstance(node.value, ast.Attribute)
        and node.value.attr == "model_config"
    )


class TestVLLMServedModelAliasRegistry(unittest.TestCase):
    def test_vllm_apps_register_each_alias_with_actual_model_path(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in VLLM_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())
                alias_helper_calls = [
                    node
                    for node in ast.walk(tree)
                    if _calls_build_base_model_paths_with_actual_model_path(node)
                ]
                served_model_names_calls = [
                    node
                    for node in alias_helper_calls
                    if _calls_build_base_model_paths_with_served_model_names(node)
                ]
                served_model_names_assignments = [
                    node for node in ast.walk(tree) if _assigns_served_model_names_from_effective_args(node)
                ]
                direct_base_model_path_calls = [
                    node for node in ast.walk(tree) if _calls_base_model_path_directly(node)
                ]

                self.assertTrue(
                    _imports_build_base_model_paths(tree),
                    f"{app_file} should import build_base_model_paths",
                )
                self.assertEqual(
                    len(alias_helper_calls),
                    1,
                    f"{app_file} should build OpenAI base model paths through build_base_model_paths",
                )
                self.assertEqual(
                    len(served_model_names_assignments),
                    1,
                    f"{app_file} should store the effective served_model_name after coercion",
                )
                self.assertEqual(
                    len(served_model_names_calls),
                    1,
                    f"{app_file} should use the effective served_model_name as alias source",
                )
                self.assertEqual(
                    direct_base_model_path_calls,
                    [],
                    f"{app_file} should not pass served_model_name directly into BaseModelPath",
                )

    def test_metrics_labels_do_not_use_raw_model_config_served_model_name(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in VLLM_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())

                self.assertFalse(
                    any(_is_model_config_served_model_name(node) for node in ast.walk(tree)),
                    "metrics labels should use normalized served_model_names, not raw vLLM model_config",
                )


if __name__ == "__main__":
    unittest.main()
