import argparse, json, time, threading, datetime
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
p=argparse.ArgumentParser();p.add_argument('--root',required=True);p.add_argument('--port',type=int,default=19092);args=p.parse_args()
root=Path(args.root);lock=threading.Lock()
def log(event):
    event.update(t=time.monotonic_ns()/1e9,utc=datetime.datetime.now(datetime.timezone.utc).isoformat())
    with lock:
        with (root/'backend.jsonl').open('a') as f:f.write(json.dumps(event)+'\n')
class Handler(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def do_GET(self):
        data=b'{"object":"list","data":[{"id":"test-model","object":"model"}]}'
        self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        case,role=self.path.strip('/').split('/')[:2];rid=self.headers.get('X-Rto-ID')
        modes=json.loads((root/'modes.json').read_text());mode=modes.get(case,'healthy') if role=='A' else 'healthy'
        log({'kind':'received','id':rid,'case':case,'role':role,'model':body.get('model'),'mode':mode})
        if mode=='read':time.sleep(400)
        if mode=='healthy' or mode=='read':code=200
        else:code=int(mode)
        payload={'id':'rto-test','object':'chat.completion','model':body['model'],'choices':[{'index':0,'message':{'role':'assistant','content':role},'finish_reason':'stop'}],'usage':{'prompt_tokens':1,'completion_tokens':1,'total_tokens':2}}
        if code!=200:payload={'error':{'message':'controlled fault '+str(code),'type':'server_error'}}
        try:
            if code==200:time.sleep(.02)
            self.send_response(code)
            if body.get('stream') and code==200:
                self.send_header('Content-Type','text/event-stream');self.send_header('Connection','close');self.end_headers()
                log({'kind':'headers','id':rid,'role':role,'status':code})
                self.wfile.write(('data: '+json.dumps(payload)+'\n\n').encode());self.wfile.flush()
                log({'kind':'first_content','id':rid,'role':role})
                time.sleep(.04);self.wfile.write(b'data: [DONE]\n\n');self.wfile.flush();self.close_connection=True
            else:
                data=json.dumps(payload).encode();self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers()
                log({'kind':'headers','id':rid,'role':role,'status':code})
                self.wfile.write(data);self.wfile.flush()
            log({'kind':'complete','id':rid,'role':role,'status':code})
        except (BrokenPipeError,ConnectionResetError):log({'kind':'client_closed','id':rid,'role':role})
class Server(ThreadingHTTPServer):request_queue_size=128;daemon_threads=True
log({'kind':'start','port':args.port})
Server(('0.0.0.0',args.port),Handler).serve_forever()
