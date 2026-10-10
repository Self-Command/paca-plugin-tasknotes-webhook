"""Reproduce the user's blocked confirmation and history cleanup in official Paca."""
schema='plugin_data_com_selfcommand_tasknotes_webhook'
def history_sql(sql):
    result=subprocess.run(['docker','exec','-i','paca-ci-db','psql','-X','-q','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d','paca'],input='SET search_path TO '+schema+';\n'+sql,text=True,capture_output=True)
    if result.returncode:raise RuntimeError(result.stderr)
    lines=result.stdout.strip().splitlines()
    return lines[-1] if lines else ''
hp=request('POST','/projects',{'name':'清单与历史回归','task_id_prefix':'HIS'},201)['data']
hs=request('GET',f'/projects/{hp["id"]}/task-statuses')['data']['items']
hm={'open':next(s['id'] for s in hs if s['category']=='todo'),'done':next(s['id'] for s in hs if s['category']=='done')}
hc=request('POST',f'/plugins/{plugin_id}/projects/{hp["id"]}/connections',{'name':'历史清理验证','status_map':hm},201)
ha=f'/plugins/{plugin_id}/projects/{hp["id"]}/connections/{hc["id"]}'
ht=request('POST',ha+'/pairing',{},201)['token']
hcore=[request('POST',f'/projects/{hp["id"]}/tasks',{'title':title,'status_id':hm['open']},201)['data'] for title in ['有效任务一','有效任务二']]
request('PUT',ha+'/sync-config',{'mode':'preview','revision':hc['revision']})
def wait_history_ready():
    for _ in range(180):
        value=request('GET',ha+'/sync-preview')
        if value['ready']:return value
        time.sleep(.5)
    raise AssertionError(value)
assert int(wait_history_ready()['total'])==2
hids=[str(uuid.uuid4()) for _ in range(4)]
hpaths=['旧删除历史一.md','旧删除历史二.md','旧删除历史三.md','仍在处理的删除历史.md']
for i,(oid,path) in enumerate(zip(hids,hpaths)):
    snapshot=json.dumps({'title':path.removesuffix('.md'),'path':path,'dateCreated':'2026-10-01T00:00:00Z','details':'需要移除的旧正文'},ensure_ascii=False)
    sid=history_sql(f"INSERT INTO sources(connection_id,vault_key,source_key,external_ref,state,snapshot) VALUES('{hc['id']}','fixture','{path}','history:{oid}','deleted','{snapshot}'::jsonb) RETURNING id;")
    history_sql(f"INSERT INTO sync_objects(id,connection_id,source_id,source_ref,deleted,snapshot,paca_snapshot,path,note_created,last_error) VALUES('{oid}','{hc['id']}',{sid},'history:{oid}',TRUE,'{snapshot}','{snapshot}','{path}','2026-10-01T00:00:00Z','周期日期与笔记来源不一致'); INSERT INTO sync_changes(connection_id,object_id,revision,payload) VALUES('{hc['id']}','{oid}',1,jsonb_build_object('sync_id','{oid}','task_id','','revision',1,'kind','task','deleted',TRUE,'path','{path}','note_created','2026-10-01T00:00:00Z','source_ref','history:{oid}','snapshot','{snapshot}'::jsonb,'warnings',jsonb_build_array('周期日期与笔记来源不一致')));")
history_sql(f"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body,state,next_attempt) VALUES('{hc['id']}','{hids[3]}','fixture','preserve-pending','hash','{{}}','pending',NOW()+INTERVAL '1 day');")
replay_id='cleared-delivery-'+str(uuid.uuid4())
replay_raw=json.dumps({'event':'task.deleted','timestamp':'2026-10-01T00:00:00Z','vault':{'name':'Fixture','path':'/fixture'},'data':{'task':{'path':hpaths[0],'dateCreated':'2026-10-01T00:00:00Z','title':'旧标题'}}},ensure_ascii=False,separators=(',',':')).encode()
replay_hash=hashlib.sha256(replay_raw).hexdigest()
history_sql(f"INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event,state) VALUES('{hc['id']}','{replay_id}','{replay_raw.decode()}','{replay_hash}','task.deleted','applied'); INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event,state,next_attempt) VALUES('{hc['id']}','obsolete-conflict','{replay_raw.decode()}','old-hash','task.updated','conflict',NOW()+INTERVAL '1 day'),('{hc['id']}','live-conflict','{{\"data\":{{\"task\":{{\"path\":\"current.md\"}}}}}}','live-hash','task.updated','conflict',NOW()+INTERVAL '1 day');")
before=request('GET',ha+'/sync-preview')
assert before['ready'] and int(before['total'])==2 and int(before['history_total'])==4 and int(before['warning_count'])==0,before
assert all(not row['deleted'] for row in before['items'])
with sync_playwright() as hpw:
    hb=hpw.chromium.launch();hctx=hb.new_context(viewport={'width':1440,'height':1000})
    assert hctx.request.post('http://127.0.0.1:18081/api/v1/auth/login',data={'username':'admin','password':new_password}).ok
    hpage=hctx.new_page();hpage.goto(f'http://127.0.0.1:18081/projects/{hp["id"]}/settings/',wait_until='domcontentloaded')
    hpage.get_by_role('button',name=manifest['displayName'],exact=True).last.click(timeout=45000)
    enable=hpage.get_by_role('button',name='启用双向同步（2 个任务）',exact=True)
    expect(enable).to_be_enabled(timeout=30000)
    for testid in ['deleted-task-history','receive-history','source-history']:
        assert not hpage.get_by_test_id(testid).evaluate('(el)=>el.open'),testid
    expect(hpage.get_by_text('旧删除历史一',exact=True)).not_to_be_visible()
    hpage.screenshot(path=str(verification/'tasknotes-current-settings.png'),full_page=True)
    enable.click();expect(hpage.get_by_text('已启用',exact=True)).to_be_visible()
    assert all(not x.get('warnings') for x in sync_request('GET','/changes?after=0',token=ht)['items'] if x['deleted'])
    hpage.get_by_role('button',name='清理旧历史',exact=True).click()
    modal=hpage.get_by_role('dialog',name='清理旧历史',exact=True);expect(modal).to_be_visible()
    bounds=modal.bounding_box();assert abs(bounds['x']+bounds['width']/2-720)<2 and abs(bounds['y']+bounds['height']/2-500)<2,bounds
    assert hpage.get_by_role('button',name='确认清理',exact=True).bounding_box()['height']>=44
    hpage.screenshot(path=str(verification/'tasknotes-history-confirmation.png'),full_page=True)
    hpage.get_by_role('button',name='取消',exact=True).click();expect(modal).not_to_be_visible()
    assert int(request('GET',ha+'/sync-preview')['history_total'])==4
    review=request('GET',ha+'/history-cleanup');assert [int(review[k]) for k in ['deleted_tasks','processed_events','obsolete_sources']]==[3,2,3],review
    # A stale review never removes newer rows.
    history_sql(f"INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event,state) VALUES('{hc['id']}','after-review','{{}}','new-hash','task.updated','applied');")
    cleared=request('POST',ha+'/history-cleanup',{'cutoff':review['cutoff'],'confirm':True})
    assert [int(cleared[k]) for k in ['deleted_tasks','processed_events','obsolete_sources']]==[3,2,3],cleared
    # Exercise the actual confirmation button for the remaining post-review event.
    hpage.get_by_role('button',name='清理旧历史',exact=True).click();expect(modal).to_be_visible()
    hpage.get_by_role('button',name='确认清理',exact=True).click();expect(modal).not_to_be_visible(timeout=30000)
    expect(hpage.get_by_text('已清理 0 条已删除任务历史、1 条接收记录和 0 条失效关联。',exact=True)).to_be_visible()
    hb.close()
remaining=request('GET',ha+'/sync-preview');assert int(remaining['total'])==2 and int(remaining['history_total'])==1,remaining
assert [x['delivery_id'] for x in request('GET',ha+'/deliveries')['items']]==['live-conflict']
assert history_sql(f"SELECT bool_and(snapshot='{{}}'::jsonb AND paca_snapshot='{{}}'::jsonb AND last_error='') FROM sync_objects WHERE connection_id='{hc['id']}' AND history_cleared_at IS NOT NULL;")=='t'
assert history_sql(f"SELECT bool_and(body='{{}}'::jsonb AND body_hash<>'') FROM inbox WHERE connection_id='{hc['id']}' AND history_cleared_at IS NOT NULL;")=='t'
assert history_sql(f"SELECT COUNT(*) FROM sync_operations WHERE connection_id='{hc['id']}' AND op_id='preserve-pending' AND state='pending';")=='1'
for row in hcore:assert request('GET',f'/projects/{hp["id"]}/tasks/{row["id"]}')['data']['title']==row['title']
def history_feed(after=0):
    req=urllib.request.Request(f'http://127.0.0.1:18092/task-sync/v1/changes?after={after}',headers={'Authorization':'Bearer '+ht,'X-Sync-Device':str(uuid.uuid4())})
    with urllib.request.urlopen(req) as response:return json.load(response)
hf=history_feed();old=[x for x in hf['items'] if x['sync_id'] in hids[:3]]
assert len(old)==6 and all(x['deleted'] and not x.get('warnings') and x['snapshot']=={} for x in old),old
assert any(x['revision']==2 for x in old)
# Same delivery hash remains idempotent even though its old body has been removed.
receive_url=base+'/plugins/'+plugin_id+'/receive/'+hc['id']
receiver_secret=hc['secret']
replay_req=urllib.request.Request(receive_url,data=replay_raw,method='POST',headers={'Content-Type':'application/json','X-TaskNotes-Event':'task.deleted','X-TaskNotes-Delivery-ID':replay_id,'X-TaskNotes-Signature':hmac.new(receiver_secret.encode(),replay_raw,hashlib.sha256).hexdigest()})
with urllib.request.urlopen(replay_req) as response:assert response.status==200 and json.load(response)['duplicate']
assert int(request('GET',ha+'/sync-preview')['total'])==2
wrong=f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{hc["id"]}/history-cleanup'
request('GET',wrong,expected=404);request('POST',wrong,{'cutoff':review['cutoff'],'confirm':True},404)
request('POST',ha+'/history-cleanup',{'cutoff':review['cutoff'],'confirm':False},400)
try:urllib.request.urlopen(base+ha+'/history-cleanup');raise AssertionError('Unauthenticated cleanup scope was readable')
except urllib.error.HTTPError as denied:assert denied.code in (401,403)
# Empty projects may be enabled after a complete scan; an unscanned project cannot.
ep=request('POST','/projects',{'name':'空项目同步回归','task_id_prefix':'EMP'},201)['data']
ec=request('POST',f'/plugins/{plugin_id}/projects/{ep["id"]}/connections',{'name':'空项目来源'},201)
ea=f'/plugins/{plugin_id}/projects/{ep["id"]}/connections/{ec["id"]}'
request('PUT',ea+'/sync-config',{'mode':'enabled','revision':ec['revision']},409)
request('PUT',ea+'/sync-config',{'mode':'preview','revision':ec['revision']})
for _ in range(180):
    ev=request('GET',ea+'/sync-preview')
    if ev['ready']:break
    time.sleep(.5)
else:raise AssertionError(ev)
assert int(ev['total'])==0
current=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{ep["id"]}/connections')['items'] if c['id']==ec['id'])
request('PUT',ea+'/sync-config',{'mode':'enabled','revision':current['revision']})
(verification/'settings-history-report.json').write_text(json.dumps({'official_paca':'0.18.6','deleted_warnings_do_not_block':True,'current_tasks_only':True,'history_collapsed':True,'actual_enable_button':True,'confirmation_centered':True,'cancel_preserves_history':True,'actual_cleanup_button':True,'review_cutoff_preserved':True,'current_tasks_preserved':True,'pending_operation_preserved':True,'live_conflict_preserved':True,'details_redacted':True,'offline_tombstones_without_warnings':True,'replay_remains_idempotent':True,'connection_scope':True,'authentication_required':True,'empty_project_can_enable':True,'unscanned_project_cannot_enable':True},ensure_ascii=False,indent=2))
