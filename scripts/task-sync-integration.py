"""Bidirectional API verification inside the pinned official Paca Action host."""
import uuid

sync_project=request('POST','/projects',{'name':'双向任务验证','task_id_prefix':'SYNC'},201)['data']
sync_archive=request('POST',f'/projects/{sync_project["id"]}/task-statuses',{'name':'归档','category':'done','position':99},201)['data']
sync_states=request('GET',f'/projects/{sync_project["id"]}/task-statuses')['data']['items']
sync_mapping={'open':next(s['id'] for s in sync_states if s['category']=='todo'),'in-progress':next(s['id'] for s in sync_states if s['category']=='inprogress'),'done':next(s['id'] for s in sync_states if s['category']=='done' and s['id']!=sync_archive['id']),'@archived':sync_archive['id']}
sync_sender_secret=secrets.token_hex(32)
sync_connection=request('POST',f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections',{'name':'双向测试来源','secret':sync_sender_secret,'status_map':sync_mapping},201)
sync_connection_id=sync_connection['id']
sync_admin=f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections/{sync_connection_id}'
sync_token=request('POST',sync_admin+'/pairing',{},201)['token']
sync_device=str(uuid.uuid4())
other_device=str(uuid.uuid4())
def sync_request(method,path,data=None,expected=200,device=None,token=None):
    req=urllib.request.Request('http://127.0.0.1:18092/task-sync/v1'+path,data=None if data is None else json.dumps(data).encode(),method=method,headers={'Authorization':'Bearer '+(token or sync_token),'X-Sync-Device':device or sync_device,'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=20) as r: status,payload=r.status,r.read()
    except urllib.error.HTTPError as e: status,payload=e.code,e.read()
    assert status==expected,(method,path,status,payload.decode())
    return json.loads(payload)

def sync_config(mode):
    entry=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections')['items'] if c['id']==sync_connection_id)
    request('PUT',sync_admin+'/sync-config',{'mode':mode,'revision':entry['revision']})

def feed(): return sync_request('GET','/changes?after=0')['items']
def sync_item(task_id):
    for _ in range(180):
        items=[c for c in feed() if c['task_id']==task_id]
        if items: return items[-1]
        time.sleep(.5)
    raise AssertionError('Task not exported by complete reconciliation')
def wait_operation(operation,state='applied'):
    for _ in range(90):
        result=sync_request('GET','/operations/'+operation['op_id'])
        if result['state']==state: return result
        if result['state'] in ('failed','conflict','uncertain') and result['state']!=state: raise AssertionError(result)
        time.sleep(.5)
    raise AssertionError(result)
def submit(item,changes,kind='update',expected=202):
    op={'op_id':str(uuid.uuid4()),'sync_id':item['sync_id'],'base_revision':item['revision'],'kind':kind,'base':item['snapshot'],'changes':changes}
    sync_request('POST','/operations',op,expected)
    return op

sync_config('preview')
native=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'Paca 页面和 AI 共用的创建接口','status_id':sync_mapping['open'],'tags':['同步测试']},201)['data']
proxy=ThreadingHTTPServer(('127.0.0.1',18182),Proxy)
threading.Thread(target=proxy.serve_forever,daemon=True).start()
sync_env={**worker_env,'PACA_API_URL':'http://127.0.0.1:18182','LISTEN_ADDR':'127.0.0.1:18092'}
sync_log=open(verification/'task-sync-worker.log','w')
sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
try:
    for _ in range(120):
        try:
            preview=request('GET',sync_admin+'/sync-preview')
            if int(preview['total'])==1: break
        except (OSError,RuntimeError): pass
        time.sleep(.5)
    else: raise AssertionError('Initial preview did not complete')
    assert sync_request('GET','/changes?after=0')['items']==[],'Preview changed a vault before confirmation'
    sync_config('enabled')
    original=sync_item(native['id'])
    assert original['snapshot']['status']=='open',original
    assert original['snapshot']['scheduled'] is None
    sync_request('GET','/info',token='0'*64,expected=401)
    sync_request('POST','/bindings',{'sync_id':'invalid','action':'claim'},400)
    claimed=sync_request('POST','/bindings',{'sync_id':original['sync_id'],'action':'claim'})
    assert claimed['state']=='claimed'
    waiting=sync_request('POST','/bindings',{'sync_id':original['sync_id'],'action':'claim'},device=other_device)
    assert waiting['state']=='pending','Second device stole an unconfirmed creation'
    sync_request('POST','/bindings',{'sync_id':original['sync_id'],'action':'confirm','path':'Tasks/Paca.md','note_created':'2026-10-08T00:00:00Z'})
    assert sync_request('POST','/bindings',{'sync_id':original['sync_id'],'action':'claim'},device=other_device)['path']=='Tasks/Paca.md'
    receipt=sync_request('POST','/receipts',{'sync_id':original['sync_id'],'revision':original['revision'],'op_id':str(uuid.uuid4()),'expected':original['snapshot']},201)
    sync_request('POST','/receipts/'+receipt['id']+'/ack',{'snapshot':{**original['snapshot'],'title':'错误写入'},'path':'Tasks/Paca.md','note_created':'2026-10-08T00:00:00Z'},409)
    sync_request('POST','/receipts/'+receipt['id']+'/ack',{'snapshot':original['snapshot'],'path':'Tasks/Paca.md','note_created':'2026-10-08T00:00:00Z'})
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{native["id"]}',{'title':'Paca 修改标题'})
    op=submit(original,{'priority':'high'})
    wait_operation(op)
    core=request('GET',f'/projects/{sync_project["id"]}/tasks/{native["id"]}')['data']
    assert core['title']=='Paca 修改标题' and core['importance']==75,'Different fields were not merged'
    same=sync_request('POST','/operations',op)
    assert same['state']=='applied'
    sync_request('POST','/operations',{**op,'changes':{'priority':'low'}},409)
    conflict=submit(original,{'title':'Obsidian 修改标题'})
    conflict_result=wait_operation(conflict,'conflict')
    conflicts=sync_request('GET','/conflicts')['items']
    assert any(row['fields']==['title'] for row in conflicts)
    latest=sync_item(native['id'])
    sync_request('POST','/conflicts/'+conflict_result['result']['conflict_id']+'/resolve',{'keep':'server','revision':latest['revision']})
    # Explicit precise times survive the core DATE representation, other custom fields survive PATCH.
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{native["id"]}',{'custom_fields':{**core['custom_fields'],'user-extra':'保留'}})
    time_op=submit(latest,{'scheduled':'2026-10-09T09:00:00','due':'2026-10-09T10:00:00','details':'## 中文内容\n- 一项任务'})
    wait_operation(time_op)
    updated=sync_item(native['id'])
    assert updated['snapshot']['scheduled']=='2026-10-09T09:00:00'
    core=request('GET',f'/projects/{sync_project["id"]}/tasks/{native["id"]}')['data']
    assert core['custom_fields']['user-extra']=='保留'
    archived=submit(updated,{'archived':True})
    wait_operation(archived)
    unarchive=submit(sync_item(native['id']),{'archived':False})
    wait_operation(unarchive)
    assert request('GET',f'/projects/{sync_project["id"]}/tasks/{native["id"]}')['data']['status_id']!=sync_archive['id']
    # Response loss after a successful POST is reconciled by the stable external marker.
    before_creates=create_requests
    lose_next_create=True
    create={'op_id':str(uuid.uuid4()),'sync_id':str(uuid.uuid4()),'base_revision':0,'kind':'create','base':{},'changes':{'title':'离线创建一次','status':updated['snapshot']['status'],'priority':'normal','scheduled':None,'due':None,'details':'内容','tags':[],'archived':False}}
    sync_request('POST','/operations',create,202)
    result=wait_operation(create)
    assert create_requests==before_creates+1,'Lost response caused a duplicate task'
    created_id=result['result']['task_id']
    created_item=sync_item(created_id)
    request('DELETE',f'/projects/{sync_project["id"]}/tasks/{created_id}')
    for _ in range(180):
        if sync_item(created_id)['deleted']: break
        time.sleep(.5)
    else: raise AssertionError('Confirmed core deletion did not generate tombstone')
    delete_item=sync_item(native['id'])
    delete_op=submit(delete_item,{},'delete')
    wait_operation(delete_op)
    request('GET',f'/projects/{sync_project["id"]}/tasks/{native["id"]}',expected=404)
    # A delayed edit of a deleted task must not starve unrelated signed deliveries.
    late_task={**updated['snapshot'],'id':'Tasks/Paca.md','path':'Tasks/Paca.md','dateCreated':'2026-10-08T00:00:00Z','dateModified':datetime.datetime.now(datetime.timezone.utc).isoformat(),'details':'<!-- paca-sync-id:'+original['sync_id']+' -->\n延迟修改'}
    late_body={'event':'task.updated','timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat(),'vault':{'name':'Late delivery fixture','path':'/fixture'},'data':{'task':late_task,'previous':late_task}}
    late_raw=json.dumps(late_body,ensure_ascii=False,separators=(',',':')).encode()
    late_delivery='deleted-late-edit-'+str(uuid.uuid4())
    late_req=urllib.request.Request(base+'/plugins/'+plugin_id+'/receive/'+sync_connection_id,data=late_raw,method='POST',headers={'Content-Type':'application/json','X-TaskNotes-Event':'task.updated','X-TaskNotes-Delivery-ID':late_delivery,'X-TaskNotes-Signature':hmac.new(sync_sender_secret.encode(),late_raw,hashlib.sha256).hexdigest()})
    with urllib.request.urlopen(late_req,timeout=20) as response:assert response.status==202
    for _ in range(120):
        late_row=next(row for row in request('GET',sync_admin+'/deliveries')['items'] if row['delivery_id']==late_delivery)
        if late_row['state']=='conflict':break
        time.sleep(.5)
    else:raise AssertionError('A delayed deleted-task event remained in the active queue')
    # Durable changes remain across worker restart, and a revoked token cannot read them.
    cursor=sync_request('GET','/changes?after=0')['next_cursor']
    sync_process.terminate();sync_process.wait(timeout=10)
    sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
    for _ in range(50):
        try:
            assert sync_request('GET',f'/changes?after={cursor}')['next_cursor']>=cursor
            break
        except OSError: time.sleep(.3)
    request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':False})
    sync_request('GET','/info',expected=401)
    request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':True})
    # Deletion is an intent even when the final task snapshot is unchanged.
    delete_core=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'相同快照仍然删除'},201)['data']
    delete_item=sync_item(delete_core['id'])
    sync_request('POST','/bindings',{'sync_id':delete_item['sync_id'],'action':'claim'},200)
    sync_request('POST','/bindings',{'sync_id':delete_item['sync_id'],'action':'confirm','path':'Tasks/删除回归.md','note_created':'2026-10-09T00:00:00Z','revision':delete_item['revision']},200)
    final_snapshot={**delete_item['snapshot'],'id':'Tasks/删除回归.md','path':'Tasks/删除回归.md','dateCreated':'2026-10-09T00:00:00Z','dateModified':datetime.datetime.now(datetime.timezone.utc).isoformat(),'details':'<!-- paca-sync-id:'+delete_item['sync_id']+' -->'}
    delete_raw=json.dumps({'event':'task.deleted','timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat(),'vault':{'name':'Deletion fixture','path':'/fixture'},'data':{'task':final_snapshot}},ensure_ascii=False).encode()
    delete_request=urllib.request.Request(base+'/plugins/'+plugin_id+'/receive/'+sync_connection_id,data=delete_raw,method='POST',headers={'Content-Type':'application/json','X-TaskNotes-Event':'task.deleted','X-TaskNotes-Delivery-ID':'delete-same-snapshot-'+str(uuid.uuid4()),'X-TaskNotes-Signature':hmac.new(sync_sender_secret.encode(),delete_raw,hashlib.sha256).hexdigest()})
    with urllib.request.urlopen(delete_request,timeout=20) as response:assert response.status==202
    for _ in range(120):
        core_rows=request('GET',f'/projects/{sync_project["id"]}/tasks?page_size=200')['data']['items']
        if not any(row['id']==delete_core['id'] for row in core_rows):break
        time.sleep(.5)
    else:raise AssertionError('Unchanged deletion snapshot was misclassified as an echo')
    exec((ROOT/'scripts/recurrence-integration.py').read_text(),globals())
    exec((ROOT/'scripts/schedule-freeze-integration.py').read_text(),globals())
    (verification/'task-sync-report.json').write_text(json.dumps({'official_paca':'0.18.6','initial_preview':True,'native_create_exported':True,'project_scope':True,'creation_lease_not_stolen':True,'actual_write_ack':True,'different_fields_merged':True,'same_field_conflict':True,'operation_idempotent':True,'precise_time_preserved':True,'unmapped_custom_fields_preserved':True,'archive_unarchive':True,'lost_create_response_one_task':True,'native_delete_tombstone':True,'obsidian_delete_core':True,'durable_restart':True,'plugin_disable_pauses_sync':True},ensure_ascii=False,indent=2))
finally:
    try:(verification/'task-sync-diagnostic.json').write_text(json.dumps({'preview':request('GET',sync_admin+'/sync-preview'),'changes':feed(),'operations':sync_request('GET','/conflicts')},ensure_ascii=False,indent=2))
    except Exception:pass
    sync_process.terminate();sync_process.wait(timeout=10)
    sync_log.close();proxy.shutdown();proxy.server_close()
