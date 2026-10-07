"""Executed in the existing official host smoke environment, only on Actions."""
fixtures=json.loads((ROOT/'verification/tasknotes-official-fixtures.json').read_text())
for fixture in fixtures['deliveries']:
    expected_signature=hmac.new(fixtures['secret'].encode(),fixture['body'].encode(),hashlib.sha256).hexdigest()
    assert hmac.compare_digest(fixture['headers']['X-TaskNotes-Signature'],expected_signature),'official signature protocol mismatch'
connection=request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections',{'name':'Official TaskNotes fixture','timezone':'Asia/Shanghai'},201)
connection_id=connection['id']
secret=connection['secret']
api_key=request('POST','/users/me/api-keys',{'name':'TaskNotes CI worker'},201)['data']['key']
secrets_dir=ROOT/'ci-secrets'
secrets_dir.mkdir(mode=0o700,exist_ok=True)
for name,value in [('api-key',api_key),('worker-secret',worker_secret)]:
    (secrets_dir/name).write_text(value)
    (secrets_dir/name).chmod(0o600)
cmd('docker','run','-d','--name','paca-ci-db-forward','--network','paca-ci','-p','127.0.0.1:15432:5432','alpine/socat','tcp-listen:5432,fork,reuseaddr','tcp-connect:paca-ci-db:5432')
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
import threading, datetime
lose_next_create=False
create_requests=0
class Proxy(BaseHTTPRequestHandler):
    def log_message(self,*args): pass
    def forward(self):
        global lose_next_create,create_requests
        data=self.rfile.read(int(self.headers.get('Content-Length',0))) or None
        request_headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','content-length','connection')}
        forwarded=urllib.request.Request('http://127.0.0.1:18080'+self.path,data=data,method=self.command,headers=request_headers)
        try:
            with urllib.request.urlopen(forwarded,timeout=20) as r: status,payload=r.status,r.read()
        except urllib.error.HTTPError as e: status,payload=e.code,e.read()
        if self.command=='POST' and self.path.endswith('/tasks'):
            create_requests+=1
            if lose_next_create and status==201:
                lose_next_create=False
                status,payload=504,b'{"message":"simulated lost response"}'
        self.send_response(status)
        self.send_header('Content-Type','application/json')
        self.end_headers()
        self.wfile.write(payload)
    do_GET=forward
    do_POST=forward
    do_PATCH=forward
    do_DELETE=forward
proxy=ThreadingHTTPServer(('127.0.0.1',18180),Proxy)
threading.Thread(target=proxy.serve_forever,daemon=True).start()
worker_env={**os.environ,'PACA_API_URL':'http://127.0.0.1:18180','DATABASE_URL':'postgres://postgres:ci-only-password@127.0.0.1:15432/paca?sslmode=disable','PACA_API_KEY_FILE':str(secrets_dir/'api-key'),'WORKER_SECRET_FILE':str(secrets_dir/'worker-secret')}
log=open(ROOT/'verification/worker.log','w')
worker_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=worker_env,stdout=log,stderr=log)

def send_official(index,expected=202,delivery_override=None,raw_override=None):
    fixture=fixtures['deliveries'][index]
    raw=(raw_override or fixture['body']).encode()
    headers={**fixture['headers'],'X-TaskNotes-Signature':hmac.new(secret.encode(),raw,hashlib.sha256).hexdigest()}
    headers['X-TaskNotes-Event']=json.loads(raw)['event']
    if delivery_override: headers['X-TaskNotes-Delivery-ID']=delivery_override
    req=urllib.request.Request(base+f'/plugins/{plugin_id}/receive/{connection_id}',data=raw,method='POST',headers=headers)
    try:
        with opener.open(req,timeout=15) as r: status,payload=r.status,r.read()
    except urllib.error.HTTPError as e: status,payload=e.code,e.read()
    assert status==expected,(status,payload.decode())
    return json.loads(payload)

def wait_delivery(index,state='applied'):
    delivery_id=fixtures['deliveries'][index]['headers']['X-TaskNotes-Delivery-ID']
    for _ in range(60):
        rows=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/deliveries')['items']
        record=next((x for x in rows if x['delivery_id']==delivery_id),None)
        if record and record['state']==state: return record
        time.sleep(0.5)
    raise AssertionError(f'delivery never reached {state}: {rows}')

send_official(0)
send_official(0,200)
changed=json.loads(fixtures['deliveries'][0]['body'])
changed['data']['task']['title']='different duplicate body'
send_official(0,409,raw_override=json.dumps(changed))
wait_delivery(0)
associations=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items']
assert len(associations)==1
imported_id=associations[0]['task_id']
created=request('GET',f'/projects/{project["id"]}/tasks/{imported_id}')['data']
assert created['title']=='官方 TaskNotes 任务' and created['importance']==75
request('PATCH',f'/projects/{project["id"]}/tasks/{imported_id}',{'tags':created['tags']+['paca-only']})
send_official(1)
wait_delivery(1)
updated=request('GET',f'/projects/{project["id"]}/tasks/{imported_id}')['data']
assert updated['title']=='官方任务已修改' and updated['importance']==10 and 'paca-only' in updated['tags']
assert updated['custom_fields']['_integration_state_v1']['start_precision']=='instant'

# A successful core POST whose response is lost must be recovered through its external marker.
lost=json.loads(fixtures['deliveries'][0]['body'])
lost['data']['task']['path']='Tasks/Uncertain.md'
lost['data']['task']['id']='Tasks/Uncertain.md'
lost['data']['task']['title']='创建成功但响应丢失'
lost['data']['task']['scheduled']='2026-10-10'
lose_next_create=True
before_requests=create_requests
send_official(0,202,'lost-create-response',json.dumps(lost))
for _ in range(80):
    rows=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/deliveries')['items']
    record=next(x for x in rows if x['delivery_id']=='lost-create-response')
    if record['state']=='applied':break
    time.sleep(0.5)
else:raise AssertionError(f'uncertain create not reconciled: {rows}')
assert create_requests==before_requests+1,'uncertain create was blindly submitted again'
linked=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items']
lost_id=next(x['task_id'] for x in linked if x['path']=='Tasks/Uncertain.md')
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['custom_fields']['_integration_state_v1']['start_precision']=='day'

# A paired vault keeps its identity across computers and follows explicit rename metadata.
def send_case(event, task, delivery, previous=None, vault_path='/ci/other-computer'):
    stamp=datetime.datetime.now(datetime.timezone.utc).isoformat()
    task={**task,'dateModified':stamp}
    envelope={'event':event,'timestamp':stamp,'vault':{'name':'Paired vault','path':vault_path},'data':{'task':task}}
    if previous is not None: envelope['data']['previous']=previous
    send_official(0,202,delivery,json.dumps(envelope))
    for _ in range(60):
        rows=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/deliveries')['items']
        record=next(x for x in rows if x['delivery_id']==delivery)
        if record['state']=='applied': return task
        time.sleep(0.5)
    raise AssertionError(f'{delivery} was not applied: {record}')

changed={**lost['data']['task'],'title':'同一 vault 换电脑','scheduled':'2026-10-10T00:10:00+08:00','recurrence':''}
changed=send_case('task.updated',changed,'other-computer')
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['title']==changed['title']
calendar=request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']
assert calendar['start_date']=='2026-10-10T00:00:00Z'
assert calendar['custom_fields']['_integration_state_v1']['start_instant']=='2026-10-09T16:10:00Z'
assert not calendar['custom_fields']['_integration_state_v1']['recurring']
renamed={**changed,'path':'Tasks/Renamed.md','id':'Tasks/Renamed.md'}
renamed=send_case('task.updated',renamed,'rename-with-previous',changed)
assert len(request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items'])==2
renamed=send_case('task.archived',{**renamed,'archived':True},'archive')
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['custom_fields']['_integration_state_v1']['archived']
renamed=send_case('task.unarchived',{**renamed,'archived':False},'unarchive')
assert not request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['custom_fields']['_integration_state_v1']['archived']
renamed=send_case('task.completed',{**renamed,'status':'done'},'complete')
statuses=request('GET',f'/projects/{project["id"]}/task-statuses')['data']['items']
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['status_id']==next(x['id'] for x in statuses if x['category']=='done')
send_official(0,202,'stale-delivery')
time.sleep(3)
assert request('GET',f'/projects/{project["id"]}/tasks/{imported_id}')['data']['title']=='官方任务已修改'
send_official(2)
wait_delivery(2)
request('GET',f'/projects/{project["id"]}/tasks/{imported_id}',expected=404)
send_official(0,202,'old-after-delete')
time.sleep(3)
assert next(x for x in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items'] if x['path']=='Tasks/Integration.md')['state']=='deleted'
worker_process.terminate()
worker_process.wait(timeout=10)
log.close()
proxy.shutdown()
(ROOT/'verification/tasknotes-report.json').write_text(json.dumps({'official_source':fixtures['source_sha'],'signed_inbox':True,'deduplicate':True,'different_duplicate_rejected':True,'create_update_same_task':True,'preserve_unmanaged_tags':True,'stale_event':True,'delete_tombstone':True,'lost_create_response_reconciled':True,'day_precision':True,'paired_vault_identity_across_computers':True,'explicit_rename':True,'archive_unarchive':True,'completion_status':True},indent=2))
