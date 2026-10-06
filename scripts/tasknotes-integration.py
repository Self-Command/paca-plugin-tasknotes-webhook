"""Executed in the existing official host smoke environment, only on Actions."""
fixtures=json.loads((ROOT/'verification/tasknotes-official-fixtures.json').read_text())
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
worker_env={**os.environ,'PACA_API_URL':'http://127.0.0.1:18080','DATABASE_URL':'postgres://postgres:ci-only-password@127.0.0.1:15432/paca?sslmode=disable','PACA_API_KEY_FILE':str(secrets_dir/'api-key'),'WORKER_SECRET_FILE':str(secrets_dir/'worker-secret')}
log=open(ROOT/'verification/worker.log','w')
worker_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=worker_env,stdout=log,stderr=log)

def send_official(index,expected=202,delivery_override=None,raw_override=None):
    fixture=fixtures['deliveries'][index]
    raw=(raw_override or fixture['body']).encode()
    headers={**fixture['headers'],'X-TaskNotes-Signature':hmac.new(secret.encode(),raw,hashlib.sha256).hexdigest()}
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
send_official(0,202,'stale-delivery')
time.sleep(3)
assert request('GET',f'/projects/{project["id"]}/tasks/{imported_id}')['data']['title']=='官方任务已修改'
send_official(2)
wait_delivery(2)
request('GET',f'/projects/{project["id"]}/tasks/{imported_id}',expected=404)
send_official(0,202,'old-after-delete')
time.sleep(3)
assert request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources')['items'][0]['state']=='deleted'
worker_process.terminate()
worker_process.wait(timeout=10)
log.close()
(ROOT/'verification/tasknotes-report.json').write_text(json.dumps({'official_source':fixtures['source_sha'],'signed_inbox':True,'deduplicate':True,'create_update_same_task':True,'preserve_unmanaged_tags':True,'stale_event':True,'delete_tombstone':True},indent=2))
