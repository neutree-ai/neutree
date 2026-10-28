"""Static guard for the SGLang serve app's reported model identity.

``engine_args.served_model_name`` overrides the registry-level serve name, and
the engine is constructed from the *merged* argument dict, so the name the
engine actually serves can differ from the default computed before the merge.
``/v1/models`` must report the effective name, otherwise the control plane
discovers a model name that the engine will not answer to.

The app cannot be imported here (it needs Ray, FastAPI and SGLang), so these
check the source's shape. They follow the idiom of
``serve/vllm/test_served_model_alias_registry.py``.
"""

import ast
import pathlib
import unittest


SGLANG_APP_FILES = (
    pathlib.Path("v0_5_10/app.py"),
)

SERVED_NAME_KEY = "served_model_name"

MODELS_ROUTE = "/v1/models"


def _is_self_attr(node, attr):
    return (
        isinstance(node, ast.Attribute)
        and node.attr == attr
        and isinstance(node.value, ast.Name)
        and node.value.id == "self"
    )


def _references(node, names):
    return any(
        isinstance(child, ast.Name) and child.id in names for child in ast.walk(node)
    )


def _is_key(node, key):
    return isinstance(node, ast.Constant) and node.value == key


def _reads_args_key(node, key):
    """True for ``args.get(<key>, ...)`` or ``args[<key>]``."""
    if isinstance(node, ast.Subscript):
        return (
            isinstance(node.value, ast.Name)
            and node.value.id == "args"
            and _is_key(node.slice, key)
        )
    if isinstance(node, ast.Call):
        func = node.func
        return (
            isinstance(func, ast.Attribute)
            and func.attr == "get"
            and isinstance(func.value, ast.Name)
            and func.value.id == "args"
            and bool(node.args)
            and _is_key(node.args[0], key)
        )
    return False


def _reads_args_key_anywhere(node, key):
    return any(_reads_args_key(child, key) for child in ast.walk(node))


def _engine_args_merge_lineno(tree):
    """Line of ``args.update(engine_kwargs)`` — where overrides take effect."""
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        if not (
            isinstance(func, ast.Attribute)
            and func.attr == "update"
            and isinstance(func.value, ast.Name)
            and func.value.id == "args"
        ):
            continue
        if any(_references(arg, {"engine_kwargs"}) for arg in node.args):
            return node.lineno
    return None


def _engine_args_validation_lineno(tree):
    """Line of the last coercion/validation call over ``args``.

    Coercion and filtering rewrite (and drop) engine args, so the effective
    identity is only final after them.
    """
    linenos = [
        node.lineno
        for node in ast.walk(tree)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Name)
        and node.func.id in ("coerce_args", "filter_engine_args")
        and node.args
        and isinstance(node.args[0], ast.Name)
        and node.args[0].id == "args"
    ]
    return max(linenos) if linenos else None


def _served_name_assignments(tree):
    return [
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.Assign)
        and any(_is_self_attr(target, SERVED_NAME_KEY) for target in node.targets)
    ]


def _writes_name_back_to_args(node):
    """True for ``args[<key>] = self.<key>``."""
    return isinstance(node, ast.Assign) and any(
        isinstance(target, ast.Subscript)
        and isinstance(target.value, ast.Name)
        and target.value.id == "args"
        and _is_key(target.slice, SERVED_NAME_KEY)
        for target in node.targets
    ) and _is_self_attr(node.value, SERVED_NAME_KEY)


def _route_handlers(tree, route):
    """Functions decorated with ``@app.<method>(<route>)``."""
    handlers = []

    for node in ast.walk(tree):
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        for decorator in node.decorator_list:
            if (
                isinstance(decorator, ast.Call)
                and isinstance(decorator.func, ast.Attribute)
                and isinstance(decorator.func.value, ast.Name)
                and decorator.func.value.id == "app"
                and decorator.args
                and _is_key(decorator.args[0], route)
            ):
                handlers.append(node)

    return handlers


class TestSGLangServedModelIdentity(unittest.TestCase):
    def test_effective_name_is_resolved_once_from_the_merged_engine_args(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in SGLANG_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())

                merge_lineno = _engine_args_merge_lineno(tree)
                validation_lineno = _engine_args_validation_lineno(tree)
                self.assertIsNotNone(
                    merge_lineno,
                    f"{app_file} should merge engine_kwargs into the engine args",
                )
                self.assertIsNotNone(
                    validation_lineno,
                    f"{app_file} should coerce/filter the merged engine args",
                )
                effective_lineno = max(merge_lineno, validation_lineno)

                # Every assignment to the identity after the engine args are
                # final must read the merged args: the pre-merge default is a
                # different value as soon as engine_args overrides it.
                post_merge = [
                    node
                    for node in _served_name_assignments(tree)
                    if node.lineno > effective_lineno
                ]
                self.assertEqual(
                    len(post_merge),
                    1,
                    f"{app_file} should resolve the served model name exactly "
                    f"once, after the engine_args override and coercion",
                )

                resolved = post_merge[0]
                self.assertTrue(
                    _reads_args_key_anywhere(resolved.value, SERVED_NAME_KEY),
                    f"{app_file} should take the served model name from "
                    f"args[{SERVED_NAME_KEY!r}], not from the pre-merge default",
                )

                # The Engine is built from `args`, so the reported identity has
                # to be written back for the two to agree even when the
                # override is empty or null and the default wins.
                self.assertTrue(
                    any(
                        _writes_name_back_to_args(node) and node.lineno > resolved.lineno
                        for node in ast.walk(tree)
                    ),
                    f"{app_file} should feed the resolved name back into the "
                    f"engine args, so the Engine serves exactly the name that "
                    f"/v1/models reports",
                )

    def test_models_route_serves_the_stored_identity_as_the_model_id(self):
        base_dir = pathlib.Path(__file__).parent

        for app_file in SGLANG_APP_FILES:
            with self.subTest(app_file=str(app_file)):
                tree = ast.parse((base_dir / app_file).read_text())

                model_info_returns = [
                    node
                    for node in ast.walk(tree)
                    if isinstance(node, ast.Return)
                    and isinstance(node.value, ast.Dict)
                    and any(
                        _is_key(key, SERVED_NAME_KEY)
                        and _is_self_attr(value, SERVED_NAME_KEY)
                        for key, value in zip(node.value.keys, node.value.values)
                    )
                ]
                self.assertEqual(
                    len(model_info_returns),
                    1,
                    f"{app_file} should expose the stored effective identity "
                    f"as {SERVED_NAME_KEY!r} for the models route to read",
                )

                handlers = _route_handlers(tree, MODELS_ROUTE)
                self.assertEqual(
                    len(handlers),
                    1,
                    f"{app_file} should serve {MODELS_ROUTE}",
                )

                # data[0].id is what clients and the control plane read to
                # learn the model's name; serving a path or the registry name
                # here is the defect this guards against.
                ids = [
                    (key, value)
                    for node in ast.walk(handlers[0])
                    if isinstance(node, ast.Dict)
                    for key, value in zip(node.keys, node.values)
                    if _is_key(key, "id")
                ]
                self.assertTrue(
                    any(
                        isinstance(value, ast.Subscript)
                        and isinstance(value.value, ast.Name)
                        and value.value.id == "info"
                        and _is_key(value.slice, SERVED_NAME_KEY)
                        for _, value in ids
                    ),
                    f"{app_file} should serve {MODELS_ROUTE} data[0].id from "
                    f"info[{SERVED_NAME_KEY!r}], not from a path or the "
                    f"control-plane-side name",
                )


if __name__ == "__main__":
    unittest.main()
