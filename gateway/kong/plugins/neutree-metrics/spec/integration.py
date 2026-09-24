#!/usr/bin/env python3
"""Opt-in integration test for the independent metrics plugin. Creates one temporary EE using CLI and cleans it up.
Run on the Docker host. Supply CLI, API/GATEWAY/METRICS/VM URLs,
MOCK_HOST (reachable by Kong), and ADMIN_PASSWORD_FILE. No stored API keys read.
"""
import concurrent.futures
import http.server
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

API = os.environ['NEUTREE_SERVER_URL'].rstrip('/')
GATEWAY = os.environ['GATEWAY_URL'].rstrip('/')
METRICS = os.environ['METRICS_URL']
VM = os.environ['VM_QUERY_URL'].rstrip('/')
CLI = os.environ['NEUTREE_CLI']
ADMIN = os.environ["KONG_ADMIN_URL"].rstrip("/")
HOLD = threading.Event()
PORT = int(os.environ.get('MOCK_PORT', '18489'))
NAME = 'monitoring-e2e-' + str(int(time.time()))
SCOPE = '/workspace/default/external-endpoint/' + NAME
PREFIX = 'kong_neutree_route_'


def request(url, data=None, token=None, method=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    body = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=45) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def api(path, data=None, token=None, method=None):
    code, body = request(API + path, data, token, method)
    assert code < 300, (path, code, body[:200])
    return json.loads(body) if body else None


def wait(check, timeout=30):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        value = check()
        if value:
            return value
        time.sleep(.25)
    raise AssertionError('Timed out waiting for expected state')


def samples(name):
    code, body = request(METRICS)
    assert code == 200, (code, body[:150])
    rows = []
    for line in body.decode().splitlines():
        if not line.startswith(PREFIX + name + '{'):
            continue
        label_text, value = line.split('{', 1)[1].rsplit('}', 1)
        labels = {k: json.loads(v) for k, v in re.findall(r'(\w+)=("(?:\\.|[^"\\])*")', label_text)}
        if labels.get('endpoint') == SCOPE:
            rows.append((labels, float(value)))
    return rows


def total(name, **labels):
    return sum(v for row, v in samples(name) if all(row.get(k) == value for k, value in labels.items()))


class Mock(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        data = json.dumps({'object': 'list', 'data': [{'id': 'mock-a'}, {'id': 'mock-b'}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        instruction = data.get('messages', [{}])[0].get('content', '')
        if instruction == 'error':
            self.send_response(503)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"error":{"message":"controlled upstream failure"}}')
            return
        if instruction == 'hold':
            HOLD.wait(30)
        if instruction == 'slow':
            time.sleep(3)
        self.send_response(200)
        stream = data.get('stream', False)
        self.send_header('Content-Type', 'text/event-stream' if stream else 'application/json')
        self.end_headers()
        try:
            if stream:
                for i in range(12):
                    chunk = {'id': 'monitor-e2e', 'object': 'chat.completion.chunk', 'model': data['model'],
                             'choices': [{'index': 0, 'delta': {'content': 'ok'}, 'finish_reason': None}]}
                    self.wfile.write(('data: ' + json.dumps(chunk) + '\n\n').encode())
                    self.wfile.flush()
                    time.sleep(.1)
                self.wfile.write(b'data: [DONE]\n\n')
            else:
                self.wfile.write(json.dumps({'id': 'monitor-e2e', 'object': 'chat.completion', 'model': data['model'],
                    'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'ok'}, 'finish_reason': 'stop'}],
                    'usage': {'prompt_tokens': 2, 'completion_tokens': 1, 'total_tokens': 3}}).encode())
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    status, data = request(ADMIN + '/')
    assert status == 200
    kong_config = json.loads(data)['configuration']
    config_settle = kong_config['db_update_frequency'] + kong_config['db_update_propagation'] + 1
    if kong_config['worker_consistency'] == 'eventual':
        config_settle += kong_config['worker_state_update_frequency']
    server = http.server.ThreadingHTTPServer(('0.0.0.0', PORT), Mock)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    password = Path(os.environ['ADMIN_PASSWORD_FILE']).read_text().strip()
    jwt = api('/api/v1/auth/token?grant_type=password', {'email': os.environ.get('ADMIN_EMAIL', 'admin@neutree.local'), 'password': password})['access_token']
    key = api('/api/v1/rpc/create_api_key', {'p_workspace': 'default', 'p_name': NAME, 'p_quota': 0, 'p_expires_in': 3600}, jwt)
    if isinstance(key, list):
        key = key[0]
    token = key['status']['sk_value']
    env = dict(os.environ, NEUTREE_API_KEY=token)

    def cli(*args):
        result = subprocess.run([CLI, *args], env=env, capture_output=True, text=True)
        assert result.returncode == 0, result.stderr[:600] + result.stdout[:600]
        return result.stdout

    def chat(model='fixed', content='ok', stream=False, path='/v1/chat/completions'):
        return request(GATEWAY + SCOPE + path, {'model': model, 'messages': [{'role': 'user', 'content': content}], 'max_tokens': 8, 'stream': stream}, token)[0]

    def target(name='a', limit=0, weight=1, priority=0):
        return {'upstream': name, 'upstream_model': 'mock-' + name, 'max_inflight_requests': limit, 'weight': weight, 'priority': priority}

    manifest = {'apiVersion': 'v1', 'kind': 'ExternalEndpoint', 'metadata': {'name': NAME, 'workspace': 'default'},
        'spec': {'timeout': 30000, 'upstreams': [{'name': name, 'upstream': {'url': f'http://{os.environ["MOCK_HOST"]}:{PORT}/v1'}, 'models': ['mock-' + name], 'model_mapping': {}} for name in ['a', 'b']],
                 'model_routes': [
                     {'model': 'fixed', 'strategy': 'fixed', 'targets': [target()]},
                     {'model': 'limited', 'strategy': 'fixed', 'targets': [target(limit=1)]},
                     {'model': 'weighted', 'strategy': 'weighted', 'targets': [target(weight=40), target('b', weight=60)]},
                     {'model': 'priority', 'strategy': 'priority', 'targets': [target(limit=1), target('b', priority=1)]},
                 ]}}
    created = False
    report = {'endpoint': NAME}
    try:
        with tempfile.TemporaryDirectory(prefix='routing-monitoring-') as directory:
            path = Path(directory) / 'endpoint.json'
            path.write_text(json.dumps(manifest))
            cli('version')
            cli('apply', '--force-update', '-f', str(path))
            created = True
            wait(lambda: samples('compiled_targets'), 90)
            assert chat() == 200
            assert chat(content='error') == 503
            assert chat(stream=True) == 200
            assert chat(path='/anthropic/v1/messages') == 200
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                pending = [pool.submit(chat, 'limited', 'slow') for _ in range(2)]
                wait(lambda: total('inflight', virtual_model='limited') == 1)
                report['limited_statuses'] = sorted(f.result() for f in pending)
                assert report['limited_statuses'] == [200, 503], report
                pending = [pool.submit(chat, 'fixed', 'slow') for _ in range(2)]
                wait(lambda: total('inflight', virtual_model='fixed') == 2)
                assert [f.result() for f in pending] == [200] * 2
                pending = [pool.submit(chat, 'priority', 'slow') for _ in range(2)]
                wait(lambda: total('inflight', virtual_model='priority', upstream='a') == 1 and total('inflight', virtual_model='priority', upstream='b') == 1)
                assert [f.result() for f in pending] == [200] * 2
                assert list(pool.map(lambda _: chat('weighted'), range(20))) == [200] * 20
            for i in range(15):
                assert chat('unrecognized-' + str(i)) == 400
            wait(lambda: total('inflight') == 0)
            wait(lambda: total('completed_requests_total') == 45)
            report['completed'] = total('completed_requests_total')
            report['success_duration_count'] = total('request_duration_seconds_count')
            assert report['success_duration_count'] == 28, report
            report['unknown_models'] = sorted({row['virtual_model'] for row, value in samples('completed_requests_total') if value > 0 and row['virtual_model'].startswith(('unrecognized', 'unknown'))})
            assert report['unknown_models'] == ['unknown'], report
            assert total('completed_requests_total', virtual_model='unknown', status_code='400') == 15
            assert total('completed_requests_total', virtual_model='limited', upstream='', status_code='503') == 1
            # Model listing is outside the completion metric boundary.
            assert request(GATEWAY + SCOPE + '/v1/models', token=token)[0] == 200
            time.sleep(2)
            assert total('completed_requests_total') == 45
            # Client cancellation must release the existing admission lease.
            payload = {'model': 'limited', 'messages': [{'role': 'user', 'content': 'ok'}], 'stream': True, 'max_tokens': 8}
            req = urllib.request.Request(GATEWAY + SCOPE + '/v1/chat/completions', data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
            with urllib.request.urlopen(req, timeout=20) as response:
                assert response.status == 200
                response.readline()
            wait(lambda: total('inflight', virtual_model='limited') == 0)
            wait(lambda: total('completed_requests_total') == 46)
            report['cancel_released'] = True
            report['completed'] = total('completed_requests_total')
            query = 'sum(' + PREFIX + 'completed_requests_total{endpoint=' + json.dumps(SCOPE) + ',gateway_instance!=""})'
            def vm_matches():
                code, body = request(VM + '/api/v1/query?' + urllib.parse.urlencode({'query': query, 'nocache': '1'}))
                result = json.loads(body).get('data', {}).get('result', [])
                return code == 200 and result and float(result[0]['value'][1]) == 46
            wait(vm_matches, 120)
            report['victoriametrics_matches'] = True
            failure_query = 'sum by (status_code) (' + PREFIX + 'completed_requests_total{endpoint=' + json.dumps(SCOPE) + ',status_code!~"2[0-9][0-9]"})'
            code, body = request(VM + '/api/v1/query?' + urllib.parse.urlencode({'query': failure_query, 'nocache': '1'}))
            failures = {row['metric']['status_code']: float(row['value'][1]) for row in json.loads(body)['data']['result']}
            assert code == 200 and failures == {'400': 15, '503': 2}, failures
            report['failure_codes_in_victoriametrics'] = failures
            report['weighted_targets'] = {row['upstream']: value for row, value in samples('completed_requests_total') if row['virtual_model'] == 'weighted' and value > 0}
            # Auth can reject before Gateway.access; resolve only endpoint identity.
            assert request(GATEWAY + SCOPE + '/v1/chat/completions', {'model': 'fixed'})[0] == 401
            wait(lambda: total('completed_requests_total', virtual_model='unknown', status_code='401') == 1)
            assert request(GATEWAY + SCOPE + '/v1/models')[0] == 401
            time.sleep(1.2)
            assert total('completed_requests_total', virtual_model='unknown', status_code='401') == 1
            report['early_auth_and_model_list'] = True

            # A later producer may deny an already-selected target. One logical
            # request is recorded, and Gateway still releases its own lease.
            status, data = request(ADMIN + '/consumers?' + urllib.parse.urlencode({'custom_id': key['id']}))
            assert status == 200
            consumer = json.loads(data)['data'][0]
            assert consumer['custom_id'] == str(key['id'])
            consumer_id = consumer['id']
            status, data = request(ADMIN + '/consumers/' + consumer_id + '/plugins',
                                   {'name': 'neutree-ai-access', 'config': {'disabled': True}})
            assert status == 201
            access_id = json.loads(data)['id']
            try:
                time.sleep(config_settle)
                assert chat() == 403
                wait(lambda: total('completed_requests_total', virtual_model='fixed', upstream='a', status_code='403') == 1)
                wait(lambda: total('inflight') == 0)
                assert request(ADMIN + '/plugins/' + access_id, {'config': {'disabled': False, 'rate_limits': [{'limit': 0, 'window': 'second'}]}}, method='PATCH')[0] == 200
                time.sleep(config_settle)
                assert chat() == 429
                wait(lambda: total('completed_requests_total', virtual_model='fixed', upstream='a', status_code='429') == 1)
                wait(lambda: total('inflight') == 0)
            finally:
                request(ADMIN + '/plugins/' + access_id, method='DELETE')
            time.sleep(config_settle)
            report['later_access_rejections'] = [403, 429]

            # Config changes are visible on the next scrape, including old live leases.
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(chat, 'limited', 'hold')
                wait(lambda: total('inflight', virtual_model='limited') == 1)
                original_routes = manifest['spec']['model_routes']
                manifest['spec']['model_routes'] = [r for r in original_routes if r['model'] != 'limited']
                path.write_text(json.dumps(manifest)); cli('apply', '--force-update', '-f', str(path))
                wait(lambda: not any(r['virtual_model'] == 'limited' for r, v in samples('compiled_targets')), 60)
                assert total('inflight', virtual_model='limited') == 1
                assert not any(r['virtual_model'] == 'limited' for r, v in samples('inflight_limit'))
                HOLD.set()
                assert pending.result() == 200
            wait(lambda: total('inflight') == 0)
            manifest['spec']['model_routes'] = original_routes
            path.write_text(json.dumps(manifest)); cli('apply', '--force-update', '-f', str(path))
            wait(lambda: any(r['virtual_model'] == 'limited' for r, v in samples('compiled_targets')), 60)
            report['config_change_and_draining'] = True

            # Disable the consumer, leaving producer routing and lease release intact.
            status, data = request(ADMIN + '/plugins/neutree-metrics')
            assert status == 200
            plugin = json.loads(data)
            before = total('completed_requests_total')
            try:
                assert request(ADMIN + '/plugins/' + plugin['id'], {'enabled': False}, method='PATCH')[0] == 200
                time.sleep(config_settle)
                assert chat('limited') == 200
                wait(lambda: total('inflight') == 0)
                time.sleep(1.2)
                assert total('completed_requests_total') == before
            finally:
                assert request(ADMIN + '/plugins/' + plugin['id'], {'enabled': True}, method='PATCH')[0] == 200
            time.sleep(config_settle)
            assert chat('limited') == 200
            wait(lambda: total('completed_requests_total') == before + 1)
            report['metrics_disabled_routing_unchanged'] = True
            if os.environ.get('REAL_ENDPOINT_SCOPE'):
                real = os.environ['REAL_ENDPOINT_SCOPE']
                report['real_upstream_statuses'] = []
                for model, stream in [('test-qwen', False), ('test-qwen', True), ('test-model-weighted', False)]:
                    code, _ = request(GATEWAY + real + '/v1/chat/completions', {
                        'model': model, 'messages': [{'role': 'user', 'content': 'Reply OK.'}],
                        'max_tokens': 8, 'stream': stream,
                    }, token)
                    report['real_upstream_statuses'].append(code)
                    assert code == 200, (model, code)
                code, _ = request(GATEWAY + real + '/v1/chat/completions', {
                    'model': 'monitoring-e2e-unknown-model',
                    'messages': [{'role': 'user', 'content': 'ok'}],
                }, token)
                assert code == 400, code
                report['real_unknown_model_status'] = code
            report['final_completed'] = total('completed_requests_total')
            print(json.dumps(report, indent=2), flush=True)
    finally:
        HOLD.set()
        if created:
            cli('delete', 'ExternalEndpoint', NAME, '-w', 'default')
        rows = api('/api/v1/api_keys?id=eq.' + str(key['id']), token=jwt)
        if rows:
            metadata = rows[0]['metadata']
            metadata['deletion_timestamp'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            api('/api/v1/api_keys?id=eq.' + str(key['id']), {'metadata': metadata}, jwt, 'PATCH')
        server.shutdown()


if __name__ == '__main__':
    main()
