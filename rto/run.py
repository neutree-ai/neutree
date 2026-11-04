import argparse, concurrent.futures, datetime, http.client, json, os, re, shutil, subprocess, threading, time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--root',required=True);p.add_argument('--image',default='registry.smtx.io/neutree-ai-smtx/kong/kong:3.9');args=p.parse_args()
root=Path(args.root);root.mkdir(parents=True,exist_ok=False);root.chmod(0o755)
repo=Path(__file__).resolve().parents[1];name='neutree-rto-'+root.name[-12:];lock=threading.Lock()
def event(e):
    e['utc']=datetime.datetime.now(datetime.timezone.utc).isoformat()
    with lock:
        with (root/'client.jsonl').open('a') as f:f.write(json.dumps(e)+'\n')
    print(json.dumps(e),flush=True)
configs=[('http503',503,60000,60000,5,8,False),('http429',429,60000,60000,5,8,False),('refused','refused',60000,60000,5,5,False),('read1000','read',1000,60000,5,3,False),('read60000','read',60000,60000,5,1,False),('blackhole3000','blackhole',60000,3000,5,2,False),('blackhole3000-r0','blackhole',60000,3000,0,2,False),('blackhole60000','blackhole',60000,60000,5,1,False),('stream503',503,60000,60000,5,5,True),('stream-read1000','read',1000,60000,5,2,True),('stream-blackhole3000','blackhole',60000,3000,5,1,True),('baseline','healthy',60000,60000,5,40,False)]
modes={c[0]:c[1] for c in configs};(root/'modes.json').write_text(json.dumps(modes))
observer=root/'kong/plugins/rto-observer';observer.mkdir(parents=True)
shutil.copy2(Path(__file__).with_name('observer.lua'),observer/'handler.lua');(observer/'schema.lua').write_text('return {name="rto-observer",fields={{config={type="record",fields={}}}}}')
services=[]
for case,mode,read,connect,retries,n,stream in configs:
    targets=[{'name':role,'host':'172.17.0.1','port':19093 if role=='A' and mode=='blackhole' else 19094 if role=='A' and mode=='refused' else 19092,'scheme':'http','path':'/'+case+'/'+role+'/v1'} for role in ['A','B']]
    routes=[{'model':'trial-'+str(i),'targets':[{'upstream':role,'upstream_model':'test-'+str(i),'priority':j} for j,role in enumerate(['A','B'])]} for i in range(n)]
    services.append({'name':case,'host':'172.17.0.1','port':19092,'protocol':'http','read_timeout':read,'connect_timeout':connect,'retries':retries,'routes':[{'name':case,'paths':['/'+case],'strip_path':True,'response_buffering':False}], 'plugins':[{'name':'rto-observer','config':{}},{'name':'neutree-ai-gateway','config':{'route_prefix':'/'+case,'upstreams':targets,'model_routes':routes}}]})
(root/'kong.json').write_text(json.dumps({'_format_version':'3.0','services':services}))
(root/'metadata.json').write_text(json.dumps({'source':subprocess.check_output(['git','-C',str(repo),'rev-parse','HEAD'],text=True).strip(),'image':args.image,'scenarios':configs,'clock':'CLOCK_MONOTONIC on the same Linux host; UTC labels retained separately'},indent=2))
backend_log=(root/'backend-process.log').open('w');backend=subprocess.Popen(['python3',str(Path(__file__).with_name('backend.py')),'--root',str(root)],stdout=backend_log,stderr=backend_log)
cmd=['docker','run','-d','--name',name,'--memory','768m','--cpus','2','-p','127.0.0.1:19100:8000','-p','127.0.0.1:19101:8001','-v',str(repo/'gateway')+':/gateway:ro','-v',str(root)+':/rto:ro']
for k,v in {'KONG_DATABASE':'off','KONG_DECLARATIVE_CONFIG':'/rto/kong.json','KONG_NGINX_WORKER_PROCESSES':'4','KONG_ADMIN_LISTEN':'0.0.0.0:8001','KONG_ADMIN_GUI_LISTEN':'off','KONG_STATUS_LISTEN':'off','KONG_PLUGINS':'bundled,neutree-ai-gateway,rto-observer','KONG_LUA_PACKAGE_PATH':'/gateway/?.lua;/rto/?.lua;;','KONG_NGINX_HTTP_LUA_SHARED_DICT':'neutree_ai_gateway_inflight 10m'}.items():cmd+=['-e',k+'='+v]
rule=None

def call(case,i,kind,stream=False):
    rid=case+'-'+str(i)+'-'+kind;body={'model':'trial-'+str(i),'messages':[{'role':'user','content':'hello'}],'max_tokens':8,'stream':stream}
    conn=http.client.HTTPConnection('127.0.0.1',19100,timeout=420);t0=time.monotonic_ns()/1e9
    conn.request('POST','/'+case+'/v1/chat/completions',json.dumps(body),{'Content-Type':'application/json','X-Rto-ID':rid})
    resp=conn.getresponse();th=time.monotonic_ns()/1e9;first=None
    if stream and resp.status==200:
        payload=b''
        while True:
            line=resp.readline()
            if not line:break
            payload+=line
            if first is None and line.startswith(b'data: ') and b'[DONE]' not in line:first=time.monotonic_ns()/1e9
    else:payload=resp.read()
    end=time.monotonic_ns()/1e9;conn.close()
    result={'kind':'request','id':rid,'case':case,'trial':i,'phase':kind,'stream':stream,'t0':t0,'headers':th,'first_content':first,'end':end,'status':resp.status,'ttfh_ms':(th-t0)*1000,'ttft_ms':None if first is None else (first-t0)*1000,'total_ms':(end-t0)*1000,'body':payload.decode()[:800]}
    event(result);return result

def run_case(config):
    case,mode,read,connect,retries,n,stream=config
    for i in range(n):
        if case=='baseline':assert call(case,i,'healthy',stream)['status']==200;continue
        a=call(case,i,'fault',stream)
        assert a['status']==(int(mode) if isinstance(mode,int) else 502 if mode=='refused' else 504),(case,a['status'])
        b=call(case,i,'retry',stream);assert b['status']==200 and 'B' in b['body'],case
        event({'kind':'retry_e2e','case':case,'trial':i,'retry_gap_ms':(b['t0']-a['end'])*1000,'total_ms':(b['end']-a['t0'])*1000})
    # Recovery is measured on cases whose primary can be changed to healthy.
    if mode in [503,429,'read']:
        elapsed=time.monotonic_ns()/1e9-a['end'];remaining=max(0,15.1-elapsed)
        if remaining:time.sleep(remaining)
        with lock:
            modes[case]='healthy';temp=root/'modes.tmp';temp.write_text(json.dumps(modes));temp.replace(root/'modes.json')
        probe=call(case,n-1,'probe',stream);assert probe['status']==200 and 'A' in probe['body']
        event({'kind':'recovery','case':case,'after_failure_ms':(probe['end']-a['end'])*1000,'probe_total_ms':probe['total_ms']})
try:
    subprocess.run(cmd+[args.image],check=True,capture_output=True)
    for _ in range(150):
        try:
            conn=http.client.HTTPConnection('127.0.0.1',19101,timeout=1);conn.request('GET','/status');resp=conn.getresponse();resp.read();conn.close()
            if resp.status==200:break
        except OSError:pass
        time.sleep(.2)
    else:raise RuntimeError('Kong not ready')
    info=json.loads(subprocess.check_output(['docker','inspect',name],text=True))[0];ip=info['NetworkSettings']['IPAddress']
    rule=['INPUT','-s',ip,'-d','172.17.0.1','-p','tcp','--dport','19093','-m','comment','--comment',name,'-j','DROP']
    subprocess.run(['iptables','-I']+rule[:1]+['1']+rule[1:],check=True)
    event({'kind':'blackhole_rule','source':ip,'destination':'172.17.0.1:19093','action':'DROP'})
    # Concurrent scenarios use distinct health keys; long default timeouts do
    # not block the remaining measurements. Baseline is measured separately.
    run_case(configs[-1])
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        fs=[pool.submit(run_case,c) for c in configs[:-1]]
        for f in concurrent.futures.as_completed(fs):f.result()
    event({'kind':'suite_complete','passed':True})
finally:
    if rule:
        counters=subprocess.run(['iptables','-L','INPUT','-nvx','--line-numbers'],capture_output=True,text=True)
        (root/'blackhole-counter.txt').write_text('\n'.join(x for x in counters.stdout.splitlines() if name in x))
        subprocess.run(['iptables','-D']+rule,check=True)
    logs=subprocess.run(['docker','logs',name],capture_output=True,text=True);(root/'kong.log').write_text(logs.stdout+logs.stderr)
    with (root/'gateway.jsonl').open('w') as out:
        for line in (logs.stdout+logs.stderr).splitlines():
            if 'RTO_EVENT ' in line:
                raw=line.split('RTO_EVENT ',1)[1]
                value,end=json.JSONDecoder().raw_decode(raw);out.write(json.dumps(value)+'\n')
    subprocess.run(['docker','rm','-f',name],capture_output=True)
    backend.terminate();backend.wait();backend_log.close()
    event({'kind':'cleanup','isolated_kong_removed':True,'blackhole_rule_removed':True})
