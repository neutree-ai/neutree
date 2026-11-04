"""Persistent review fixture. Modes are controlled by review-modes.json."""
import concurrent.futures as futures
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json, threading, time, urllib.request, urllib.error, datetime
from pathlib import Path

ROOT = Path('/root/workspace/cooldown-deploy-20261104')
PROVIDER = (ROOT / 'provider-key').read_text().strip()
NAME = 'cooldown-acceptance-20261104'
API = 'http://127.0.0.1:3000/api/v1/'
GATEWAY = 'http://127.0.0.1/workspace/default/external-endpoint/' + NAME
modes, events, results = {}, [], []
gate = threading.Event()

def http(url, body=None, token=None, method=None, timeout=90):
    headers = {'Content-Type': 'application/json'}
    if token: headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp: return resp.status, resp.read()
    except urllib.error.HTTPError as exc: return exc.code, exc.read()

class Proxy(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *args): pass
    def reply(self, code, payload, content_type='application/json'):
        self.send_response(code); self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(payload))); self.end_headers()
        try: self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError): pass
    def do_GET(self):
        self.reply(200, json.dumps({'object':'list','data':[{'id':'deepseek-v4.1-flash','object':'model'}]}).encode())
    def do_POST(self):
        group, role = self.path.strip('/').split('/')[:2]
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        modes = json.loads((ROOT / 'review-modes.json').read_text())
        mode = modes.get(group+role, 'real')
        print(json.dumps({'time':datetime.datetime.now(datetime.timezone.utc).isoformat(),'group':group,'role':role,'model':body.get('model'),'mode':mode}),flush=True)
        if isinstance(mode,int):
            self.reply(mode,json.dumps({'error':{'message':'controlled test failure','type':'server_error'}}).encode()); return
        if mode == 'hold': gate.wait(10); mode='mock'
        if mode == 'mock':
            self.reply(200,json.dumps({'id':'dev-test','object':'chat.completion','model':body['model'],'choices':[{'index':0,'message':{'role':'assistant','content':role},'finish_reason':'stop'}],'usage':{'prompt_tokens':1,'completion_tokens':1,'total_tokens':2}}).encode()); return
        assert body['model'] in ['deepseek-v4.1-flash','gpt-6-luna'], body['model']
        # Forward unchanged to the real provider, including streaming responses.
        code, payload = http('https://newapi.smartx.com/v1/chat/completions',body,PROVIDER)
        self.reply(code,payload,'text/event-stream' if body.get('stream') and code==200 else 'application/json')

class Server(ThreadingHTTPServer): request_queue_size = 128
server=Server(('172.19.0.1',19091),Proxy)
print('Review fixture listening on 172.19.0.1:19091',flush=True)
server.serve_forever()
