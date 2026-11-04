"""Retained live EEs; timestamped calls. No deletion and no secrets in logs."""
import argparse,concurrent.futures,datetime,http.client,json,time,urllib.request,urllib.error
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--root',required=True);args=p.parse_args();root=Path(args.root);root.mkdir(exist_ok=False)
secrets=Path('/root/workspace/cooldown-deploy-20261104');key=(secrets/'review-api-key').read_text().strip();provider=(secrets/'provider-key').read_text().strip()
API='http://127.0.0.1:3000/api/v1/'
def api(path,body=None,token=None):
    req=urllib.request.Request(API+path,data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json',**({'Authorization':'Bearer '+token} if token else {})})
    with urllib.request.urlopen(req,timeout=30) as resp:return json.load(resp) if resp.status!=204 else None
TOKEN=api('auth/token?grant_type=password',{'email':'admin@neutree.local','password':Path('/root/workspace/.neutree-test/credentials/admin-password').read_text().strip()})['access_token']
def log(e):
    e['utc']=datetime.datetime.now(datetime.timezone.utc).isoformat()
    with (root/'requests.jsonl').open('a') as f:f.write(json.dumps(e)+'\n')
    print(json.dumps(e),flush=True)
def create(name,timeout,real=False):
    rows=api('external_endpoints?metadata->>name=eq.'+name,token=TOKEN)
    assert not rows,'Preserve existing EE: '+name
    if real:
        upstreams=[{'name':'provider','upstream':{'url':'https://newapi.smartx.com/v1'},'auth':{'type':'bearer','credential':provider},'model_mapping':{}}]
        routes=[{'model':'failover-'+str(i),'strategy':'priority','targets':[{'upstream':'provider','upstream_model':m,'priority':j} for j,m in enumerate(['deepseek-v4.1-flash','gpt-6-luna'])]} for i in range(3)]
        routes.append({'model':'gpt','strategy':'fixed','targets':[{'upstream':'provider','upstream_model':'gpt-6-luna'}]})
    else:
        upstreams=[];routes=[]
        for case in ['http503','http429','refused','read','stream503']:
            for role in ['A','B']:
                port=19094 if case=='refused' and role=='A' else 19095
                upstreams.append({'name':case+role,'upstream':{'url':'http://172.19.0.1:'+str(port)+'/'+case+'/'+role+'/v1'},'model_mapping':{}})
            for i in range(3):
                routes.append({'model':case+'-'+str(i),'strategy':'priority','targets':[{'upstream':case+role,'upstream_model':'test-'+str(i),'priority':j} for j,role in enumerate(['A','B'])]})
    spec={'upstreams':upstreams,'model_routes':routes}
    if timeout:spec['timeout']=timeout
    api('external_endpoints',{'api_version':'v1','kind':'ExternalEndpoint','metadata':{'name':name,'workspace':'default'},'spec':spec},TOKEN)
    for _ in range(90):
        row=api('external_endpoints?metadata->>name=eq.'+name,token=TOKEN)[0]
        if (row.get('status') or {}).get('phase')=='Running':break
        time.sleep(1)
    else:raise AssertionError('EE not Running')
    # Kong DB polling is asynchronous; confirm POST route with invalid model,
    # which does not dispatch to either upstream or alter target health.
    for _ in range(30):
        result=call(name,'not-configured','readiness',False)
        if result['status']==400:break
        time.sleep(1)
    else:raise AssertionError('POST route not ready')
    log({'kind':'created','name':name,'id':row['id'],'service_url':row['status']['service_url'],'retained':True})
    return row

def call(name,model,phase,stream=False):
    rid=name+'-'+model+'-'+phase
    conn=http.client.HTTPConnection('127.0.0.1',80,timeout=130);t0=time.monotonic_ns()/1e9
    conn.request('POST','/workspace/default/external-endpoint/'+name+'/v1/chat/completions',json.dumps({'model':model,'messages':[{'role':'user','content':'Say hello in one short sentence.'}],'max_tokens':64,'stream':stream}),{'Content-Type':'application/json','Authorization':'Bearer '+key,'X-Rto-ID':rid})
    response=conn.getresponse();headers=time.monotonic_ns()/1e9;first=None
    if stream and response.status==200:
        body=b''
        while True:
            line=response.readline()
            if not line:break
            body+=line
            if first is None and line.startswith(b'data: ') and b'[DONE]' not in line:first=time.monotonic_ns()/1e9
    else:body=response.read()
    end=time.monotonic_ns()/1e9;conn.close()
    result={'kind':'request','id':rid,'endpoint':name,'model':model,'phase':phase,'t0':t0,'headers':headers,'end':end,'status':response.status,'ttfh_ms':(headers-t0)*1000,'ttft_ms':None if first is None else (first-t0)*1000,'total_ms':(end-t0)*1000,'body':body.decode()}
    log(result);return result

def retry(name,model,expected,stream=False):
    a=call(name,model,'fault',stream);assert a['status']==expected,(name,model,a['status'])
    b=call(name,model,'retry',stream);assert b['status']==200,(name,model,b['status'])
    log({'kind':'retry_e2e','endpoint':name,'model':model,'retry_gap_ms':(b['t0']-a['end'])*1000,'total_ms':(b['end']-a['t0'])*1000})

mock='cooldown-rto-1s';real='cooldown-rto-real'
create(mock,1000)
for case,status in [('http503',503),('http429',429),('refused',502),('read',504),('stream503',503)]:
    for i in range(3):retry(mock,case+'-'+str(i),status,case=='stream503')
create(real,90000,True)
for i in range(3):
    assert call(real,'gpt','baseline-'+str(i))['status']==200
    retry(real,'failover-'+str(i),503)
log({'kind':'complete','passed':True,'retained':[mock,real]})
