#!/usr/bin/env python3
"""Exercise a real EE and check selected-request counter deltas and idle inflight.

Requires a quiet model route: unrelated requests during the test invalidate exact counts.
Does not deploy, change EE configuration, restart containers, or create an API key.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request


def get(url):
    with urllib.request.urlopen(url, timeout=15) as response:
        return response.read().decode()


def samples(text, name, endpoint, model):
    result = {}
    for line in text.splitlines():
        if not line.startswith(name + '{'):
            continue
        label_text, value = line[len(name) + 1:].rsplit('}', 1)
        labels = {k: json.loads(v) for k, v in re.findall(r'(\w+)=("(?:\\.|[^"\\])*")', label_text)}
        if labels.get('endpoint') == endpoint and labels.get('virtual_model') == model:
            result[(labels['upstream'], labels['upstream_model'])] = float(value.strip())
    return result


def wait_for(check, timeout=20):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        value = check()
        if value:
            return value
        time.sleep(0.5)
    raise AssertionError('Timed out waiting for the expected metrics')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--gateway', required=True, help='Gateway base URL, e.g. http://host:80')
    parser.add_argument('--metrics', required=True, help='Reachable Kong status URL ending /metrics/neutree')
    parser.add_argument('--query-url', required=True, help='VictoriaMetrics Prometheus base URL')
    parser.add_argument('--endpoint', default='/workspace/default/external-endpoint/model-aggr')
    parser.add_argument('--model', required=True)
    parser.add_argument('--count', type=int, default=10)
    parser.add_argument('--concurrency', type=int, default=2)
    args = parser.parse_args()
    if args.count < 1 or args.concurrency < 1:
        parser.error('count and concurrency must be positive')
    token = os.environ.get('ENDPOINT_API_KEY')
    if not token:
        parser.error('Set ENDPOINT_API_KEY; it is not printed or written to disk')
    counter = 'kong_neutree_route_requests_total'
    gauge = 'kong_neutree_route_inflight'

    def read(name):
        return samples(get(args.metrics), name, args.endpoint, args.model)

    initial_gauges = read(gauge)
    assert initial_gauges, 'No inflight series: check plugin configuration and model name'
    assert sum(initial_gauges.values()) == 0, 'Model has existing in-flight traffic'
    before = read(counter)
    outcomes = []
    peaks = []

    def request(_):
        body = json.dumps({'model': args.model, 'messages': [{'role': 'user', 'content': 'Reply OK.'}],
                           'max_tokens': 8, 'stream': False}).encode()
        req = urllib.request.Request(args.gateway.rstrip('/') + args.endpoint + '/v1/chat/completions',
                                     data=body, headers={'Content-Type': 'application/json',
                                                         'Authorization': 'Bearer ' + token})
        try:
            with urllib.request.urlopen(req, timeout=120) as response:
                response.read()
                return response.status
        except urllib.error.HTTPError as error:
            error.read()
            return error.code

    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        pending = [pool.submit(request, i) for i in range(args.count)]
        while not all(f.done() for f in pending):
            peaks.append(sum(read(gauge).values()))
            time.sleep(0.2)
        outcomes = [f.result() for f in pending]
    assert all(200 <= status < 300 for status in outcomes), f'Requests failed: {outcomes}'
    wait_for(lambda: sum(read(gauge).values()) == 0)
    wait_for(lambda: sum(read(counter).values()) - sum(before.values()) == args.count)
    after = read(counter)
    # Query the specific scrape job, rather than accepting a similarly named old series.
    selector = '{job="neutree-gateway",endpoint=' + json.dumps(args.endpoint) + ',virtual_model=' + json.dumps(args.model) + '}'
    def stored():
        query = counter + selector
        payload = json.loads(get(args.query_url.rstrip('/') + '/api/v1/query?' + urllib.parse.urlencode({'query': query})))
        assert payload['status'] == 'success', payload
        rows = payload['data']['result']
        observed = {(row['metric']['upstream'], row['metric']['upstream_model']): float(row['value'][1])
                    for row in rows}
        return rows and observed == after
    wait_for(stored, timeout=30)
    report = {'model': args.model, 'statuses': outcomes,
              'observed_peak_inflight': max(peaks, default=0),
              'counter_deltas': [{'upstream': k[0], 'upstream_model': k[1], 'requests': v-before.get(k, 0)}
                                 for k, v in after.items()],
              'inflight_returned_to_zero': True, 'victoriametrics_matches': True}
    assert report['observed_peak_inflight'] > 0, 'No active request was sampled; concurrent gauge verification is inconclusive'
    print(json.dumps(report, indent=2, ensure_ascii=False))


if __name__ == '__main__':
    main()
