import asyncio
import json
import os
import unittest
from unittest import mock

try:
    import fastapi  # noqa: F401
except ModuleNotFoundError:
    HAS_FASTAPI = False
else:
    HAS_FASTAPI = True

from neutree.pd_router.runtime import (
    EngineHTTPResponse,
    PDRouter,
    RouterConfig,
    create_app,
    extract_route_indices,
)


def _config(engine="vllm"):
    return {
        "engine": engine,
        "engine_version": "v0.test",
        "workspace": "default",
        "endpoint": "chat",
        "router": {"port": 8000, "health_path": "/health"},
        "containers": [
            {
                "name": "prefill-0",
                "role": "prefill",
                "rank": 0,
                "http_port": 8100,
                "side_channel_port": 9100,
            },
            {
                "name": "decode-0",
                "role": "decode",
                "rank": 0,
                "http_port": 8200,
                "side_channel_port": 9200,
            },
        ],
    }


def _config_with_ports(engine="vllm"):
    config = _config(engine)
    config["ports"] = [
        {
            "router": [{"http": 8000}],
            "prefill": [{"http": 8100, "side_channel": 9100}],
            "decode": [{"http": 8200, "side_channel": 9200}],
        },
        {
            "router": [{"http": 8010}],
            "prefill": [{"http": 8110, "side_channel": 9110}],
            "decode": [{"http": 8210, "side_channel": 9210}],
        },
    ]
    return config


class FakeEngineClient:
    def __init__(self, responses=None, health=None):
        self.responses = responses or {}
        self.health = health or {}
        self.posts = []

    def post_json(self, container, path, payload, stream=False):
        self.posts.append(
            {
                "container": container,
                "path": path,
                "payload": dict(payload),
                "stream": stream,
            }
        )
        key = (container.role, container.rank, stream)
        role_key = (container.role, container.rank)
        response = self.responses.get(key, self.responses.get(role_key))
        if isinstance(response, list):
            return response.pop(0)
        if response is not None:
            return response
        return EngineHTTPResponse.json({"id": f"{container.role}-{container.rank}"})

    def get_health(self, container, path="/health", timeout=0.5):
        return bool(self.health.get(container.name, True))


class CloseAwareStream:
    def __init__(self, chunks):
        self._chunks = iter(chunks)
        self.closed = False

    def __iter__(self):
        return self

    def __next__(self):
        return next(self._chunks)

    def close(self):
        self.closed = True


class FakeFuture:
    def __init__(self, result_value=None):
        self.result_value = result_value
        self.cancelled = False

    def cancel(self):
        self.cancelled = True
        return True

    def result(self):
        return self.result_value


class FakeExecutor:
    def __init__(self, results=None):
        self.results = list(results or [None])
        self.future = None
        self.submitted = []

    def submit(self, fn, *args):
        self.submitted.append((fn, args))
        result_value = self.results.pop(0) if self.results else None
        self.future = FakeFuture(result_value)
        return self.future


class ImmediateFuture:
    def __init__(self, result_value=None, exception=None):
        self.result_value = result_value
        self.exception = exception
        self.cancelled = False

    def cancel(self):
        self.cancelled = True
        return True

    def result(self):
        if self.exception is not None:
            raise self.exception
        return self.result_value


class ImmediateExecutor:
    def submit(self, fn, *args):
        try:
            return ImmediateFuture(fn(*args))
        except Exception as exc:  # noqa: BLE001
            return ImmediateFuture(exception=exc)


def _asgi_request(app, method, path, body=b"", query_string=b""):
    events = []
    received = False

    async def receive():
        nonlocal received
        if not received:
            received = True
            return {"type": "http.request", "body": body, "more_body": False}
        return {"type": "http.disconnect"}

    async def send(message):
        events.append(message)

    scope = {
        "type": "http",
        "asgi": {"version": "3.0"},
        "http_version": "1.1",
        "method": method,
        "scheme": "http",
        "path": path,
        "raw_path": path.encode("utf-8"),
        "query_string": query_string,
        "headers": [(b"host", b"testserver"), (b"content-type", b"application/json")],
        "client": ("testclient", 50000),
        "server": ("testserver", 80),
    }
    asyncio.run(app(scope, receive, send))
    start = next(event for event in events if event["type"] == "http.response.start")
    chunks = [event.get("body", b"") for event in events if event["type"] == "http.response.body"]
    return start["status"], b"".join(chunks)


class PDRouterTests(unittest.TestCase):
    def test_config_from_env_uses_engine_override_to_select_runtime_handler(self):
        with mock.patch.dict(
            os.environ,
            {
                "NEUTREE_PD_CONFIG": json.dumps(_config("vllm")),
                "NEUTREE_PD_ENGINE": "sglang",
                "NEUTREE_PD_GROUP_ID": "chat-model-abc123",
            },
            clear=True,
        ):
            config = RouterConfig.from_env()

        self.assertEqual(config.engine, "sglang")
        self.assertEqual(config.group_id, "chat-model-abc123")

    def test_config_from_env_selects_ports_by_group_rank(self):
        with mock.patch.dict(
            os.environ,
            {
                "NEUTREE_PD_CONFIG": json.dumps(_config_with_ports("vllm")),
                "NEUTREE_PD_GROUP_RANK": "1",
                "NEUTREE_PD_ROUTER_PORT": "8000",
            },
            clear=True,
        ):
            config = RouterConfig.from_env()

        self.assertEqual(config.router_port, 8010)
        self.assertEqual(config.container("prefill", 0).http_port, 8110)
        self.assertEqual(config.container("prefill", 0).side_channel_port, 9110)
        self.assertEqual(config.container("decode", 0).http_port, 8210)
        self.assertEqual(config.container("decode", 0).side_channel_port, 9210)

    def test_vllm_non_streaming_request_runs_prefill_then_decode_with_kv_params(self):
        kv_params = {
            "do_remote_prefill": True,
            "remote_block_ids": [1, 2, 3],
            "remote_engine_id": "prefill-engine",
        }
        client = FakeEngineClient(
            responses={
                ("prefill", 0): EngineHTTPResponse.json(
                    {"id": "prefill", "kv_transfer_params": kv_params}
                ),
                ("decode", 0): EngineHTTPResponse.json(
                    {"id": "decode", "choices": [{"message": {"content": "ok"}}]}
                ),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("vllm")), client)

        result = router.handle_chat(
            {
                "messages": [{"role": "user", "content": "hi"}],
                "stream": False,
                "stream_options": {"include_usage": True},
                "max_completion_tokens": 128,
                "prefill_index": 0,
                "decode_index": 0,
            },
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 200)
        self.assertEqual(json.loads(result.body)["id"], "decode")
        self.assertEqual(len(client.posts), 2)

        prefill = client.posts[0]
        self.assertEqual(prefill["container"].name, "prefill-0")
        self.assertFalse(prefill["stream"])
        self.assertEqual(prefill["payload"]["max_tokens"], 1)
        self.assertEqual(prefill["payload"]["max_completion_tokens"], 1)
        self.assertFalse(prefill["payload"]["stream"])
        self.assertNotIn("stream_options", prefill["payload"])
        self.assertNotIn("prefill_index", prefill["payload"])
        self.assertNotIn("decode_index", prefill["payload"])
        self.assertEqual(prefill["payload"]["kv_transfer_params"], {"do_remote_decode": True})

        decode = client.posts[1]
        self.assertEqual(decode["container"].name, "decode-0")
        self.assertFalse(decode["stream"])
        self.assertEqual(decode["payload"]["request_id"], prefill["payload"]["request_id"])
        self.assertEqual(decode["payload"]["kv_transfer_params"], kv_params)
        self.assertNotIn("prefill_index", decode["payload"])
        self.assertNotIn("decode_index", decode["payload"])

    def test_vllm_fails_when_prefill_response_has_no_kv_transfer_params(self):
        client = FakeEngineClient(
            responses={
                ("prefill", 0): EngineHTTPResponse.json({"id": "prefill"}),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("vllm")), client)

        result = router.handle_chat(
            {"messages": [], "stream": False},
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 502)
        body = json.loads(result.body)
        self.assertIn("missing kv_transfer_params", body["error"]["message"])
        self.assertEqual(len(client.posts), 1)

    def test_vllm_streaming_request_forwards_decode_stream_after_prefill(self):
        kv_params = {"do_remote_prefill": True, "remote_block_ids": [7]}
        client = FakeEngineClient(
            responses={
                ("prefill", 0): EngineHTTPResponse.json({"kv_transfer_params": kv_params}),
                ("decode", 0, True): EngineHTTPResponse(
                    status_code=200,
                    headers={"content-type": "text/event-stream"},
                    stream=[b"data: chunk\n\n", b"data: [DONE]\n\n"],
                ),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("vllm")), client)

        result = router.handle_chat(
            {"messages": [], "stream": True},
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 200)
        self.assertEqual(list(result.stream), [b"data: chunk\n\n", b"data: [DONE]\n\n"])
        self.assertFalse(client.posts[0]["stream"])
        self.assertTrue(client.posts[1]["stream"])
        self.assertEqual(client.posts[1]["payload"]["kv_transfer_params"], kv_params)

    def test_sglang_request_uses_selected_prefill_bootstrap_port_for_both_roles(self):
        client = FakeEngineClient(
            responses={
                ("prefill", 0): EngineHTTPResponse.json({"id": "prefill"}),
                ("decode", 0): EngineHTTPResponse.json({"id": "decode"}),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("sglang")), client)

        result = router.handle_chat(
            {
                "messages": [{"role": "user", "content": "hi"}],
                "stream": False,
                "bootstrap_room": 42,
                "prefill_index": 0,
                "decode_index": 0,
            },
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 200)
        posts_by_role = {post["container"].role: post for post in client.posts}
        self.assertEqual(posts_by_role["prefill"]["payload"]["rid"], posts_by_role["decode"]["payload"]["rid"])
        for post in posts_by_role.values():
            self.assertEqual(post["payload"]["bootstrap_host"], "127.0.0.1")
            self.assertEqual(post["payload"]["bootstrap_port"], 9100)
            self.assertEqual(post["payload"]["bootstrap_room"], 42)
            self.assertFalse(post["payload"]["stream"])
            self.assertNotIn("prefill_index", post["payload"])
            self.assertNotIn("decode_index", post["payload"])

    def test_sglang_streaming_request_drains_prefill_and_forwards_decode_stream(self):
        client = FakeEngineClient(
            responses={
                ("prefill", 0, True): EngineHTTPResponse(
                    status_code=200,
                    headers={"content-type": "text/event-stream"},
                    stream=[b"data: prefill\n\n", b"data: [DONE]\n\n"],
                ),
                ("decode", 0, True): EngineHTTPResponse(
                    status_code=200,
                    headers={"content-type": "text/event-stream"},
                    stream=[b"data: decode\n\n", b"data: [DONE]\n\n"],
                ),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("sglang")), client)

        result = router.handle_chat(
            {"messages": [], "stream": True, "bootstrap_room": 99},
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 200)
        self.assertEqual(list(result.stream), [b"data: decode\n\n", b"data: [DONE]\n\n"])
        posts_by_role = {post["container"].role: post for post in client.posts}
        self.assertTrue(posts_by_role["prefill"]["stream"])
        self.assertTrue(posts_by_role["decode"]["stream"])
        self.assertEqual(posts_by_role["prefill"]["payload"]["bootstrap_room"], 99)
        self.assertEqual(posts_by_role["decode"]["payload"]["bootstrap_room"], 99)

    def test_sglang_non_streaming_request_starts_decode_before_prefill(self):
        client = FakeEngineClient(
            responses={
                ("decode", 0): EngineHTTPResponse.json({"id": "decode"}),
                ("prefill", 0): EngineHTTPResponse.json({"id": "prefill"}),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("sglang")), client)
        router._executor = ImmediateExecutor()

        result = router.handle_chat(
            {"messages": [], "stream": False},
            prefill_index=0,
            decode_index=0,
        )

        self.assertEqual(result.status_code, 200)
        self.assertEqual([post["container"].role for post in client.posts], ["decode", "prefill"])

    def test_sglang_stream_close_cancels_pending_prefill_and_closes_decode_stream(self):
        decode_stream = CloseAwareStream([b"data: decode\n\n", b"data: [DONE]\n\n"])
        decode_response = EngineHTTPResponse(
            status_code=200,
            headers={"content-type": "text/event-stream"},
            stream=decode_stream,
        )
        client = FakeEngineClient()
        router = PDRouter(RouterConfig.from_dict(_config("sglang")), client)
        fake_executor = FakeExecutor([decode_response, None])
        router._executor = fake_executor

        result = router.handle_chat(
            {"messages": [], "stream": True},
            prefill_index=0,
            decode_index=0,
        )

        iterator = iter(result.stream)
        self.assertEqual(next(iterator), b"data: decode\n\n")
        iterator.close()
        self.assertTrue(decode_stream.closed)
        self.assertTrue(fake_executor.future.cancelled)

    def test_extract_route_indices_accepts_query_headers_or_payload_and_strips_payload_fields(self):
        payload = {"messages": [], "prefill_index": "1"}
        prefill, decode, clean = extract_route_indices(
            payload,
            headers={"x-neutree-decode-index": "2"},
            query={},
        )

        self.assertEqual(prefill, 1)
        self.assertEqual(decode, 2)
        self.assertEqual(clean, {"messages": []})

        prefill, decode, clean = extract_route_indices(
            {"messages": [], "prefill_index": 0, "decode_index": 0},
            headers={},
            query={"prefill_index": "3", "decode_index": "4"},
        )
        self.assertEqual((prefill, decode), (3, 4))
        self.assertEqual(clean, {"messages": []})

    def test_health_and_topology_report_container_readiness_by_role_and_rank(self):
        client = FakeEngineClient(health={"prefill-0": True, "decode-0": False})
        router = PDRouter(RouterConfig.from_dict(_config("vllm")), client)

        health = router.health()
        topology = router.topology()
        metrics = router.metrics()

        self.assertEqual(health.status_code, 503)
        body = json.loads(health.body)
        self.assertFalse(body["ready"])
        self.assertEqual(body["containers"]["decode-0"]["role"], "decode")
        self.assertFalse(body["containers"]["decode-0"]["ready"])

        self.assertEqual(topology["group_id"], "chat")
        self.assertEqual(topology["units"], [{"role": "prefill", "rank": 0}])
        self.assertIn('neutree_pd_router_ready{engine="vllm"', metrics)
        self.assertIn(
            'neutree_pd_router_container_ready{container="prefill-0",role="prefill",rank="0"} 1',
            metrics,
        )
        self.assertIn(
            'neutree_pd_router_container_ready{container="decode-0",role="decode",rank="0"} 0',
            metrics,
        )

    @unittest.skipUnless(HAS_FASTAPI, "fastapi is not installed")
    def test_fastapi_app_exposes_chat_completion_and_rejects_other_completion_api(self):
        client = FakeEngineClient(
            responses={
                ("prefill", 0): EngineHTTPResponse.json({"kv_transfer_params": {"blocks": [1]}}),
                ("decode", 0): EngineHTTPResponse.json({"id": "decode"}),
            }
        )
        router = PDRouter(RouterConfig.from_dict(_config("vllm")), client)
        app = create_app(router)

        status, body = _asgi_request(
            app,
            "POST",
            "/v1/chat/completions",
            body=json.dumps({"messages": [{"role": "user", "content": "hi"}]}).encode("utf-8"),
            query_string=b"prefill_index=0&decode_index=0",
        )
        unsupported_status, _ = _asgi_request(app, "POST", "/v1/completions", body=b"{}")
        topo_status, topo_body = _asgi_request(app, "GET", "/v1/topology")

        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["id"], "decode")
        self.assertEqual(unsupported_status, 404)
        self.assertEqual(topo_status, 200)
        self.assertEqual(json.loads(topo_body)["group_id"], "chat")
        self.assertEqual([post["container"].role for post in client.posts], ["prefill", "decode"])


if __name__ == "__main__":
    unittest.main()
