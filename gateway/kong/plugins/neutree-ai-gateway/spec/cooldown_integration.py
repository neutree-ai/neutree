#!/usr/bin/env python3
"""Linux/Docker acceptance: real Kong, four workers, loopback model backends.

python3 cooldown_integration.py --image kong:3.9.1
No running NeuTree service or database is used or replaced.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor, wait
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="kong:3.9.1")
    args = parser.parse_args()
    events, modes, gates = [], {}, {}

    class Backend(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *args):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            case, role = self.headers["X-Case"], self.server.role
            events.append((case, role))
            mode = self.headers.get("X-Slow") or modes.get((case, role), 200)
            if mode == "hold":
                gates[case].wait(8)
                mode = 200
            elif mode == "timeout":
                time.sleep(.5)
                mode = 200
            if (self.path != f"/{role}/v1/chat/completions"
                    or body["model"] != f"model-{role}"
                    or self.headers["Authorization"] != f"Bearer {role}"):
                mode = 409
            payload = json.dumps({"model": f"model-{role}",
                "choices": [{"message": {"role": "assistant", "content": role}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
            content_type = "application/json"
            if body.get("stream") and mode == 200:
                payload = b"data: " + payload + b"\n\ndata: [DONE]\n\n"
                content_type = "text/event-stream"
            try:
                self.send_response(mode)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
            except (BrokenPipeError, ConnectionResetError):
                pass

    def port():
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            return sock.getsockname()[1]

    class ModelServer(ThreadingHTTPServer):
        # The 24-request burst must not overflow Python's default backlog of 5
        # and create accidental connection failures on the healthy backup.
        request_queue_size = 128

    a = ModelServer(("127.0.0.1", 0), Backend)
    b = ModelServer(("127.0.0.1", 0), Backend)
    a.role, b.role = "primary", "backup"
    for server in (a, b):
        threading.Thread(target=server.serve_forever, daemon=True).start()
    proxy_port, admin_port = port(), port()
    name = "neutree-cooldown-test-" + uuid.uuid4().hex[:8]
    cases = ["lifecycle", "all", "dns", "connect", "timeout", "local", "anthropic", "sse",
             "stale", "probe_fail", "rejectprobe", "429", "502", "504"]
    services = []
    for case in cases:
        primary = {"name": "A", "host": "missing-cooldown-test.invalid" if case == "dns" else "127.0.0.1",
                   "port": 1 if case == "connect" else a.server_port, "scheme": "http", "path": "/primary/v1",
                   "auth_header": "Bearer primary"}
        services.append({"name": case, "host": "127.0.0.1", "port": a.server_port, "protocol": "http",
            "retries": 0, "read_timeout": 200 if case == "timeout" else 10000, "connect_timeout": 200,
            "routes": [{"name": case, "paths": ["/" + case], "strip_path": True, "response_buffering": False}],
            "plugins": [{"name": "local-reject", "config": {}}, {"name": "neutree-ai-gateway", "config": {
                "route_prefix": "/" + case,
                "upstreams": [primary, {"name": "B", "host": "127.0.0.1", "port": b.server_port,
                    "scheme": "http", "path": "/backup/v1", "auth_header": "Bearer backup"}],
                "model_routes": [{"model": "virtual", "max_attempts": 1, "retryable_conditions": ["http_503"],
                    "targets": [{"upstream": "A", "upstream_model": "model-primary", "priority": 0},
                                {"upstream": "B", "upstream_model": "model-backup", "priority": 1}]}]}}]})

    def request(case, stream=False, reject=False, slow=False):
        path = "/anthropic/v1/messages" if case == "anthropic" else "/v1/chat/completions"
        headers = {"Content-Type": "application/json", "X-Case": case}
        if reject:
            headers["X-Local-Reject"] = "1"
        if slow:
            headers["X-Slow"] = "hold"
        conn = http.client.HTTPConnection("127.0.0.1", proxy_port, timeout=12)
        conn.request("POST", "/" + case + path, json.dumps({"model": "virtual", "messages": [
            {"role": "user", "content": "hi"}], "stream": stream, "max_tokens": 10}), headers)
        response = conn.getresponse()
        result = response.status, response.read().decode()
        conn.close()
        return result

    def await_requests(case, role, count):
        deadline = time.monotonic() + 3
        while events.count((case, role)) < count and time.monotonic() < deadline:
            time.sleep(.01)
        assert events.count((case, role)) == count, (case, events)

    with tempfile.TemporaryDirectory(prefix="neutree-cooldown-") as temporary:
        root = Path(temporary)
        root.chmod(0o755)  # The container's Kong user must read the fixture.
        plugin = root / "kong/plugins/local-reject"
        plugin.mkdir(parents=True)
        (plugin / "schema.lua").write_text('return {name="local-reject",fields={{config={type="record",fields={}}}}}')
        (plugin / "handler.lua").write_text('return {PRIORITY=850,VERSION="0.1.0",access=function() '
            'if kong.request.get_header("X-Local-Reject") then return kong.response.exit(429,{error="local quota"}) end end}')
        (root / "kong.json").write_text(json.dumps({"_format_version": "3.0", "services": services}))
        env = {"KONG_DATABASE": "off", "KONG_DECLARATIVE_CONFIG": "/fixture/kong.json",
            "KONG_PROXY_LISTEN": f"127.0.0.1:{proxy_port}", "KONG_ADMIN_LISTEN": f"127.0.0.1:{admin_port}",
            "KONG_STATUS_LISTEN": "off", "KONG_ADMIN_GUI_LISTEN": "off", "KONG_NGINX_WORKER_PROCESSES": "4",
            "KONG_PLUGINS": "bundled,neutree-ai-gateway,local-reject",
            "KONG_LUA_PACKAGE_PATH": "/gateway/?.lua;/fixture/?.lua;;",
            "KONG_NGINX_HTTP_LUA_SHARED_DICT": "neutree_ai_gateway_inflight 10m"}
        cmd = ["docker", "run", "-d", "--name", name, "--network", "host", "--memory", "512m", "--cpus", "2",
               "-v", f"{Path(__file__).resolve().parents[4]}:/gateway:ro", "-v", f"{root}:/fixture:ro"]
        for key, value in env.items():
            cmd += ["-e", f"{key}={value}"]
        subprocess.run(cmd + [args.image], check=True, capture_output=True)
        try:
            for _ in range(150):
                try:
                    with socket.create_connection(("127.0.0.1", proxy_port), timeout=.1):
                        break
                except OSError:
                    time.sleep(.1)
            else:
                raise RuntimeError("Kong did not start")
            for case in cases:
                if case in ("local", "stale"):
                    continue
                modes[(case, "primary")] = "timeout" if case == "timeout" else int(case) if case.isdigit() else 503
                status, _ = request(case, stream=case == "sse")
                expected = 502 if case in ("dns", "connect") else 504 if case == "timeout" else int(case) if case.isdigit() else 503
                assert status == expected, (case, status, expected)
                assert events.count((case, "backup")) == 0, "the first request must not be replayed"
                status, body = request(case, stream=case == "sse")
                assert status == 200 and "backup" in body, (case, status, body)
            assert request("local", reject=True)[0] == 429
            assert "primary" in request("local")[1], "local rejection must not cool the selected target"
            modes[("all", "backup")] = 503
            assert request("all")[0] == 503
            count = len(events)
            assert request("all")[0] == 503 and len(events) == count, "all-cooling must not fail open"
            with ThreadPoolExecutor(max_workers=24) as pool:
                gates["stale"] = threading.Event()
                late = pool.submit(request, "stale", slow=True)
                await_requests("stale", "primary", 1)
                modes[("stale", "primary")] = 503
                assert request("stale")[0] == 503
                gates["stale"].set()
                assert late.result()[0] == 200
                assert "backup" in request("stale")[1]
                print("fault attribution, no replay, local rejection and all-cooling checks passed", flush=True)
                time.sleep(15.2)
                for case in ("lifecycle", "stale"):
                    modes[(case, "primary")] = "hold"
                    gates[case] = threading.Event()
                    before = events.count((case, "primary"))
                    futures = [pool.submit(request, case) for _ in range(24)]
                    done, pending = wait(futures, timeout=2)
                    assert len(done) == 23 and len(pending) == 1, (case, len(done), len(pending))
                    assert events.count((case, "primary")) == before + 1, "more than one recovery probe"
                    assert all("backup" in future.result()[1] for future in done)
                    gates[case].set()
                    assert next(iter(pending)).result()[0] == 200
                    modes[(case, "primary")] = 200
                    assert "primary" in request(case)[1]
                assert request("probe_fail")[0] == 503
                assert "backup" in request("probe_fail")[1]
                modes[("rejectprobe", "primary")] = 200
                assert request("rejectprobe", reject=True)[0] == 429
                assert "primary" in request("rejectprobe")[1], "rejected probe leaked its admission"
                print("four-worker single-probe, recovery, failed probe and neutral release checks passed", flush=True)
        finally:
            for gate in gates.values():
                gate.set()
            logs = subprocess.run(["docker", "logs", name], capture_output=True, text=True)
            subprocess.run(["docker", "rm", "-f", name], capture_output=True)
            for server in (a, b):
                server.shutdown(); server.server_close()
            if "[error]" in logs.stderr:
                # Expected transport failures are present; retain errors for diagnosis.
                print("\n".join(line for line in logs.stderr.splitlines() if "[error]" in line))


if __name__ == "__main__":
    main()
