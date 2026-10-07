"""Executed in the existing official host smoke environment, only on Actions."""
fixtures=json.loads((ROOT/'verification/tasknotes-official-fixtures.json').read_text())
for fixture in fixtures['deliveries']:
    expected_signature=hmac.new(fixtures['secret'].encode(),fixture['body'].encode(),hashlib.sha256).hexdigest()
    assert hmac.compare_digest(fixture['headers']['X-TaskNotes-Signature'],expected_signature),'official signature protocol mismatch'
archive_status=request('POST',f'/projects/{project["id"]}/task-statuses',{'name':'归档','category':'done','position':99},201)['data']
connection=request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections',{'name':'Official TaskNotes fixture','timezone':'Asia/Shanghai','secret':fixtures['secret'],'status_map':{'@archived':archive_status['id']}},201)
connection_id=connection['id']
secret=connection['secret']
assert secret==fixtures['secret'],'receiver ignored official sender-generated Secret'
connection_path=f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}'
listed=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections')['items']
assert all('secret' not in c and 'secret_enc' not in c for c in listed),'saved Secret leaked in query'
request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections',{'name':'Invalid credential','secret':'too-short'},400)

def verify_saved_secret(value, expected=400):
    raw=b'{}'
    signature=hmac.new(value.encode(),raw,hashlib.sha256).hexdigest()
    headers={'X-TaskNotes-Signature':signature,'X-TaskNotes-Event':'task.created','X-TaskNotes-Delivery-ID':'secret-validation'}
    # Correct authentication proceeds to invalid envelope rejection; no event is created.
    request('POST',f'/plugins/{plugin_id}/receive/{connection_id}',{},expected,headers)
verify_saved_secret(secret)
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
import concurrent.futures
with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
    list(pool.map(lambda _:send_official(0,200),range(12)))
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
# Simulate a schema-3 source before its first post-upgrade event is an archive move.
legacy_task={}
for key in ['id','path','title','status','priority','scheduled','due','archived','tags','dateModified','recurrence']:
    legacy_task[key]=renamed.get(key,False if key=='archived' else [] if key=='tags' else None if key=='recurrence' else '')
legacy_raw=json.dumps(legacy_task,ensure_ascii=False,separators=(',',':'))
legacy_hash=hashlib.sha256(('live\n'+legacy_raw).encode()).hexdigest()
cmd('docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-c',f"UPDATE plugin_data_com_selfcommand_tasknotes_webhook.sources SET snapshot=NULL,event_at=NULL,last_event='',snapshot_hash='{legacy_hash}' WHERE paca_task_id='{lost_id}'")
renamed=send_case('task.archived',{**renamed,'path':'Archive/Renamed.md','id':'Archive/Renamed.md','archived':True,'tags':[*renamed.get('tags',[]),'archived']},'archive')
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['status_id']==archive_status['id']
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['custom_fields']['_integration_state_v1']['archived']
renamed=send_case('task.unarchived',{**renamed,'path':'Tasks/Renamed.md','id':'Tasks/Renamed.md','archived':False,'tags':[t for t in renamed.get('tags',[]) if t!='archived']},'unarchive')
assert not request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['custom_fields']['_integration_state_v1']['archived']
renamed=send_case('task.completed',{**renamed,'status':'done'},'complete')
statuses=request('GET',f'/projects/{project["id"]}/task-statuses')['data']['items']
assert request('GET',f'/projects/{project["id"]}/tasks/{lost_id}')['data']['status_id']==next(x['id'] for x in statuses if x['category']=='done')

# An unknown update may be a rename without oldPath. Do not silently create a duplicate.
unknown={**lost,'event':'task.updated','timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat()}
unknown['data']={'task':{**lost['data']['task'],'path':'Tasks/MissingPrevious.md','id':'Tasks/MissingPrevious.md','title':'需要人工核对','dateModified':unknown['timestamp']}}
before_requests=create_requests
send_official(0,202,'missing-previous',json.dumps(unknown))
for _ in range(60):
    record=next(x for x in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/deliveries')['items'] if x['delivery_id']=='missing-previous')
    if record['state']=='conflict': break
    time.sleep(0.5)
else: raise AssertionError('Unknown path was not held for manual association')
assert create_requests==before_requests
source=next(x for x in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items'] if x['path']=='Tasks/MissingPrevious.md')
target=request('POST',f'/projects/{project["id"]}/tasks',{'title':'Verified manual target'},201)['data']
request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/link',{'source_id':source['id'],'task_id':target['id']},202)
request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/reprocess/missing-previous',{})
for _ in range(60):
    record=next(x for x in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/deliveries')['items'] if x['delivery_id']=='missing-previous')
    if record['state']=='applied': break
    time.sleep(0.5)
else: raise AssertionError('Manual association was not verified')
assert request('GET',f'/projects/{project["id"]}/tasks/{target["id"]}')['data']['title']=='需要人工核对'
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

# Import a sender-generated replacement without changing associations/history; blank preserves it.
before_history=request('GET',connection_path+'/deliveries')['items']
before_sources=request('GET',connection_path+'/sources')['items']
c=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections')['items'] if c['id']==connection_id)
replacement=secrets.token_hex(32)
request('PATCH',connection_path,{**c,'secret':replacement})
verify_saved_secret(replacement)
verify_saved_secret(secret,401)
request('PATCH',connection_path,{**c,'secret':secrets.token_hex(32)},409)
verify_saved_secret(replacement)
c=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections')['items'] if c['id']==connection_id)
request('PATCH',connection_path,{**c,'secret':''})
verify_saved_secret(replacement)
assert request('GET',connection_path+'/deliveries')['items']==before_history
assert request('GET',connection_path+'/sources')['items']==before_sources
(ROOT/'verification/tasknotes-report.json').write_text(json.dumps({'official_source':fixtures['source_sha'],'signed_inbox':True,'deduplicate':True,'concurrent_duplicate':True,'different_duplicate_rejected':True,'create_update_same_task':True,'preserve_unmanaged_tags':True,'stale_event':True,'delete_tombstone':True,'lost_create_response_reconciled':True,'day_precision':True,'paired_vault_identity_across_computers':True,'explicit_rename':True,'archive_unarchive':True,'completion_status':True,'unknown_update_requires_association':True,'manual_link_verified':True,'official_generated_secret_imported':True,'saved_secret_not_exposed':True,'secret_replacement_revision_guard':True,'empty_secret_preserves_existing':True,'secret_change_preserves_history':True},indent=2))
