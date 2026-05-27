import json
import unittest

from neutree.pd_router_sidecar.runtime import (
    EngineHTTPResponse,
    PDRouterSidecar,
    SidecarConfig,
    extract_route_indices,
)


def _config(engine="vllm"):
    return {
        "engine": engine,
        "engine_version": "v0.test",
        "workspace": "default",
        "endpoint": "chat",
        "sidecar": {"port": 8000, "health_path": "/health"},
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


class PDRouterSidecarTests(unittest.TestCase):
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("vllm")), client)

        result = sidecar.handle_chat(
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("vllm")), client)

        result = sidecar.handle_chat(
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("vllm")), client)

        result = sidecar.handle_chat(
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("sglang")), client)

        result = sidecar.handle_chat(
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("sglang")), client)

        result = sidecar.handle_chat(
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
        sidecar = PDRouterSidecar(SidecarConfig.from_dict(_config("vllm")), client)

        health = sidecar.health()
        topology = sidecar.topology()

        self.assertEqual(health.status_code, 503)
        body = json.loads(health.body)
        self.assertFalse(body["ready"])
        self.assertEqual(body["containers"]["decode-0"]["role"], "decode")
        self.assertFalse(body["containers"]["decode-0"]["ready"])

        units = {(u["role"], u["rank"]): u for u in topology["units"]}
        self.assertTrue(units[("prefill", 0)]["ready"])
        self.assertFalse(units[("decode", 0)]["ready"])


if __name__ == "__main__":
    unittest.main()
