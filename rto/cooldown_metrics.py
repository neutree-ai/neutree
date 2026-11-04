"""Dev acceptance: retained RTO EE, reversible fault modes, append-only evidence."""
import json, time, urllib.request, urllib.error, re, datetime
from pathlib import Path
root=Path('/root/workspace/cooldown-monitoring-20261104')/datetime.datetime.now().strftime('live-%Y%m%d-%H%M%S')
root.mkdir(exist_ok=False)
key=Path('/root/workspace/cooldown-deploy-20261104/review-api-key').read_text().strip()
modes=Path('/root/workspace/cooldown-rto-runs/live-fixture/modes.json')
original=modes.read_text()
endpoint='/workspace/default/external-endpoint/cooldown-rto-1s'
metric='kong_neutree_route_target_cooldowns_total'
def log(data):
 data['utc']=datetime.datetime.now(datetime.timezone.utc).isoformat()
 with (root/'events.jsonl').open('a') as f:f.write(json.dumps(data)+'\n')
 print(json.dumps(data),flush=True)
def mode(value):
 data=json.loads(modes.read_text());data['http503']=value
 temp=modes.with_suffix('.metrics.tmp');temp.write_text(json.dumps(data));temp.replace(modes)
 log({'kind':'mode','value':value})
def count(stage,expected=None):
 # Prometheus worker-local counters synchronize asynchronously.
 time.sleep(1.2)
 raw=urllib.request.urlopen('http://172.19.0.3:8100/metrics/neutree').read().decode()
 (root/(stage+'.prom')).write_text(raw)
 rows={}
 for line in raw.splitlines():
  if line.startswith(metric+'{'):
   labels={k:json.loads(v) for k,v in re.findall(r'(\w+)=("(?:\\.|[^"\\])*")',line)}
   if labels['endpoint']==endpoint and labels['virtual_model']=='http503-0':rows[labels['upstream']]=float(line.rsplit(' ',1)[1])
 log({'kind':'counter','stage':stage,'values':rows})
 if expected is not None:assert rows=={'http503A':expected,'http503B':0},rows
 return rows['http503A']
def call(stage,expected,role=None,authorized=True):
 req=urllib.request.Request('http://127.0.0.1'+endpoint+'/v1/chat/completions',data=json.dumps({'model':'http503-0','messages':[{'role':'user','content':'hello'}]}).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+(key if authorized else 'invalid')})
 try:r=urllib.request.urlopen(req,timeout=10)
 except urllib.error.HTTPError as e:r=e
 with r:status=r.status;body=r.read().decode()
 log({'kind':'request','stage':stage,'status':status,'body':body})
 assert status==expected,(stage,status)
 if role:assert json.loads(body)['choices'][0]['message']['content']==role
try:
 mode('healthy');time.sleep(16)
 call('healthy',200,'A');base=count('baseline')
 # Wait for an actual vmagent scrape to observe zero before the first event.
 time.sleep(6)
 call('local-rejection',401,authorized=False);count('local-rejection',base)
 mode(503);call('first-failure',503);count('first-failure',base+1)
 for i in range(3):call('backup-'+str(i),200,'B')
 count('backup-traffic',base+1)
 time.sleep(16);call('failed-probe',503);count('failed-probe',base+2)
 call('backup-after-probe',200,'B')
 mode('healthy');time.sleep(16);call('recovery',200,'A');call('healthy-after-recovery',200,'A');count('recovery',base+2)
 log({'kind':'complete','passed':True,'expected_delta':2,'retained':True})
finally:
 modes.write_text(original)
 log({'kind':'restore','modes_restored':True,'ee_retained':True})
