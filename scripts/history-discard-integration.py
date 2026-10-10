"""Actions-only regression for old conflicts, orphaned legacy links and replay."""
assert os.environ.get('GITHUB_ACTIONS')=='true'
import base64

birth='2026-10-01T00:00:00Z'
history_sql(f"UPDATE connections SET enabled=FALSE WHERE id='{hc['id']}';")
legacy=[]
for i in range(6):
    dead=request('POST',f'/projects/{hp["id"]}/tasks',{'title':f'已删除旧任务{i}'},201)['data']
    request('DELETE',f'/projects/{hp["id"]}/tasks/{dead["id"]}')
    path=f'历史/旧关联{i}.md';snapshot=json.dumps({'path':path,'dateCreated':birth,'title':dead['title']},ensure_ascii=False)
    sid=int(history_sql(f"INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot) VALUES('{hc['id']}','legacy','{path}','{dead['id']}','legacy:{dead['id']}','linked','{snapshot}') RETURNING id;"))
    legacy.append({'id':sid,'task_id':dead['id'],'path':path})
live_sources=[]
for i,row in enumerate(hcore):
    path=f'current{i}.md';snapshot=json.dumps({'path':path,'dateCreated':birth,'title':row['title']},ensure_ascii=False)
    sid=int(history_sql(f"INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot) VALUES('{hc['id']}','legacy','{path}','{row['id']}','live:{row['id']}','linked','{snapshot}') RETURNING id;"))
    live_sources.append(sid)
# A 403 or transient 503 must never turn a link into a deleted source.
fault_routes=['/api/v1/projects/'+hp['id']+'/tasks/'+x['task_id'] for x in legacy[:2]]
source_verification_faults.update(dict(zip(fault_routes,[403,503])))
history_sql(f"UPDATE connections SET enabled=TRUE WHERE id='{hc['id']}';")
for _ in range(240):
    rows=request('GET',ha+'/sources')['items'];mapped={int(x['id']):x for x in rows}
    if all(mapped[x['id']]['verification_error'] for x in legacy[:2]) and all(mapped[x['id']]['state']=='deleted' for x in legacy[2:]):break
    time.sleep(.5)
else:raise AssertionError(rows)
assert all(mapped[x['id']]['state']=='linked' for x in legacy[:2]),mapped
for route in fault_routes:source_verification_faults.pop(route)
request('POST',ha+'/source-recheck',{},202)
for _ in range(240):
    rows=request('GET',ha+'/sources')['items'];mapped={int(x['id']):x for x in rows}
    if all(mapped[x['id']]['state']=='deleted' for x in legacy):break
    time.sleep(.5)
else:raise AssertionError(rows)
assert len(rows)==9 and all(mapped[sid]['state']=='linked' for sid in live_sources),rows
assert history_sql(f"SELECT COUNT(*) FROM sync_objects WHERE connection_id='{hc['id']}' AND paca_task_id IN({','.join(chr(39)+x['task_id']+chr(39) for x in legacy)});")=='0','Legacy fixture accidentally had sync objects'

live_event=int(history_sql(f"SELECT id FROM inbox WHERE connection_id='{hc['id']}' AND delivery_id='live-conflict';"))
live_object=history_sql(f"SELECT id FROM sync_objects WHERE connection_id='{hc['id']}' AND paca_task_id='{hcore[0]['id']}';")
live_body=json.dumps({'event':'task.updated','timestamp':birth,'data':{'task':{'path':'current0.md','dateCreated':birth,'title':'未同步的修改'}}},ensure_ascii=False)
history_sql(f"UPDATE inbox SET body='{live_body}',body_hash='{hashlib.sha256(live_body.encode()).hexdigest()}' WHERE id={live_event};")
conflict_op=int(history_sql(f"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body,state,inbox_id) VALUES('{hc['id']}','{live_object}','fixture','discard-live-conflict','conflict-hash','{{}}','conflict',{live_event}) RETURNING id;"))
history_sql(f"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body,state,inbox_id,next_attempt) VALUES('{hc['id']}','{live_object}','fixture','discard-live-queued','queued-hash','{{}}','pending',{live_event},NOW()+INTERVAL '1 day'); INSERT INTO sync_conflicts(id,connection_id,object_id,operation_id,base_revision,base,local,remote,fields) VALUES('{uuid.uuid4()}','{hc['id']}','{live_object}',{conflict_op},1,'{{}}',jsonb_build_object('title','未同步的修改'),'{{}}',jsonb_build_array('title'));")
batch_events=[]
for i in range(2):
    body=json.dumps({'data':{'task':{'path':legacy[2]['path'],'dateCreated':birth,'title':f'旧冲突{i}'}}},ensure_ascii=False)
    eid=int(history_sql(f"INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event,state) VALUES('{hc['id']}','discard-batch-{i}','{body}','batch-{i}','task.updated','conflict') RETURNING id;"));batch_events.append(eid)
leased=int(history_sql(f"INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event,state,lease_until) VALUES('{hc['id']}','leased-conflict','{{}}','leased-hash','task.updated','conflict',NOW()+INTERVAL '5 minutes') RETURNING id;"))
lease_review=request('POST',ha+'/history-cleanup-preview',{'mode':'events','ids':[leased]})
assert int(lease_review['processed_events'])==0,lease_review
assert int(request('POST',ha+'/history-cleanup',{'mode':'events','ids':[leased],'cutoff':lease_review['cutoff'],'confirm':True})['processed_events'])==0

# Measure real local notes plus a photo attachment across every cleanup/restart.
photo=vault/'附件/清理保留照片.png';photo.parent.mkdir(exist_ok=True)
photo.write_bytes(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9T0AAAAASUVORK5CYII='))
(vault/'清理保留笔记.md').write_text('# 保留笔记\n![[附件/清理保留照片.png]]\n',encoding='utf-8')
local_files={str(p.relative_to(vault)):hashlib.sha256(p.read_bytes()).hexdigest() for p in vault.rglob('*') if p.is_file() and p.suffix in ('.md','.png','.jpg')}

with sync_playwright() as dpw:
    db=dpw.chromium.launch();dc=db.new_context(viewport={'width':1440,'height':1000})
    assert dc.request.post('http://127.0.0.1:18081/api/v1/auth/login',data={'username':'admin','password':new_password}).ok
    dp=dc.new_page();dp.goto(f'http://127.0.0.1:18081/projects/{hp["id"]}/settings/',wait_until='domcontentloaded')
    dp.get_by_role('button',name=manifest['displayName'],exact=True).last.click(timeout=45000)
    expect(dp.get_by_test_id('receive-history').locator('summary')).to_contain_text('4',timeout=30000)
    dp.get_by_test_id('receive-history').locator('summary').click()
    live_row=dp.get_by_test_id(f'delivery-row-{live_event}');live_row.get_by_role('button',name='清理',exact=True).click()
    modal=dp.get_by_role('dialog',name='清理接收记录',exact=True);expect(modal).to_be_visible()
    expect(modal.get_by_text('将放弃 1 次尚未同步到有效任务的变更，任务当前内容保留。',exact=True)).to_be_visible()
    dp.screenshot(path=str(verification/'history-discard-live-confirm.png'),full_page=True)
    modal.get_by_role('button',name='取消',exact=True).click()
    assert history_sql(f"SELECT state FROM sync_operations WHERE id={conflict_op};")=='conflict'
    live_row.get_by_role('button',name='清理',exact=True).click();modal.get_by_role('button',name='确认清理',exact=True).click()
    expect(live_row).to_have_count(0,timeout=30000)
    assert history_sql(f"SELECT COUNT(*) FROM sync_operations WHERE connection_id='{hc['id']}' AND op_id IN('discard-live-conflict','discard-live-queued') AND state='superseded' AND body='{{}}' AND body_hash<>'';")=='2'
    assert history_sql(f"SELECT state FROM sync_conflicts WHERE operation_id={conflict_op};")=='cleared'
    for eid in batch_events:dp.get_by_label(f'选择接收记录 {eid}',exact=True).check()
    dp.get_by_role('button',name='清理选中接收记录（2）',exact=True).click()
    modal.get_by_role('button',name='确认清理',exact=True).click();expect(modal).not_to_be_visible(timeout=30000)
    dp.get_by_test_id('source-history').locator('summary').click()
    for sid in live_sources:expect(dp.get_by_label(f'选择来源关联 {sid}',exact=True)).to_be_disabled()
    first=dp.get_by_test_id(f'source-row-{legacy[0]["id"]}');first.get_by_role('button',name='清理失效关联',exact=True).click()
    sm=dp.get_by_role('dialog',name='清理失效关联',exact=True);expect(sm).to_be_visible()
    sm.get_by_role('button',name='确认清理',exact=True).click();expect(first).to_have_count(0,timeout=30000)
    dp.get_by_label('选择全部失效关联',exact=True).check()
    dp.get_by_role('button',name='清理选中失效关联（6）',exact=True).click();expect(sm).to_be_visible()
    dp.screenshot(path=str(verification/'history-discard-source-batch.png'),full_page=True)
    sm.get_by_role('button',name='确认清理',exact=True).click();expect(sm).not_to_be_visible(timeout=30000)
    expect(dp.get_by_test_id('source-history').locator('summary')).to_contain_text('2')
    assert history_sql(f"SELECT state FROM sync_operations WHERE connection_id='{hc['id']}' AND op_id='preserve-pending';")=='superseded'
    dp.get_by_role('button',name='刷新记录',exact=True).click()
    expect(dp.get_by_test_id('source-history').locator('summary')).to_contain_text('2')
    # An active worker connection lock prevents the entire cleanup transaction.
    history_sql(f"UPDATE inbox SET lease_until=NULL WHERE id={leased};")
    lr=request('POST',ha+'/history-cleanup-preview',{'mode':'events','ids':[leased]})
    blocker=subprocess.Popen(['docker','exec','-i','paca-ci-db','psql','-X','-q','-U','postgres','-d','paca'],stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,text=True)
    blocker.stdin.write(f"BEGIN;\nSELECT pg_advisory_xact_lock(hashtextextended('{hc['id']}',0));\n");blocker.stdin.flush()
    for _ in range(30):
        if history_sql(f"SELECT pg_try_advisory_xact_lock(hashtextextended('{hc['id']}',0));")=='f':break
        time.sleep(.1)
    else:raise AssertionError('Concurrency fixture did not hold the worker lock')
    try:request('POST',ha+'/history-cleanup',{'mode':'events','ids':[leased],'cutoff':lr['cutoff'],'confirm':True},409)
    finally:blocker.stdin.write('ROLLBACK;\n');blocker.stdin.close();blocker.wait(timeout=10)
    request('POST',ha+'/history-cleanup',{'mode':'events','ids':[leased],'cutoff':lr['cutoff'],'confirm':True})
    request('POST',ha+'/history-cleanup-preview',{'mode':'sources','ids':live_sources})
    denied=request('POST',ha+'/history-cleanup',{'mode':'sources','ids':live_sources,'cutoff':lr['cutoff'],'confirm':True});assert int(denied['obsolete_sources'])==0
    wrong=f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{hc["id"]}/history-cleanup-preview'
    request('POST',wrong,{'mode':'events','ids':[live_event]},404)
    request('POST',ha+'/history-cleanup-preview',{'mode':'events','ids':[live_event,live_event]},400)
    # Same body with the original or a replacement delivery ID stays cleared.
    for delivery,expected in [(replay_id,200),(replay_id+'-replacement',202)]:
        replay_req=urllib.request.Request(receive_url,data=replay_raw,method='POST',headers={'Content-Type':'application/json','X-TaskNotes-Event':'task.deleted','X-TaskNotes-Delivery-ID':delivery,'X-TaskNotes-Signature':hmac.new(receiver_secret.encode(),replay_raw,hashlib.sha256).hexdigest()})
        with urllib.request.urlopen(replay_req) as reply:assert reply.status==expected and json.load(reply)['state']=='cleared'
    sync_process.terminate();sync_process.wait(timeout=10)
    sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
    cmd('docker','restart','paca-ci-api')
    for _ in range(60):
        try:
            if request('GET',f'/plugins/{plugin_id}/health')['schema_version']==8:break
        except (OSError,RuntimeError):pass
        time.sleep(.5)
    else:raise AssertionError('Official host did not restart')
    dp.reload(wait_until='domcontentloaded');dp.get_by_role('button',name=manifest['displayName'],exact=True).last.click(timeout=45000)
    expect(dp.get_by_test_id('source-history').locator('summary')).to_contain_text('2',timeout=30000)
    expect(dp.get_by_test_id('receive-history').locator('summary')).to_contain_text('0')
    dp.screenshot(path=str(verification/'history-discard-after-restart.png'),full_page=True)
    db.close()
assert request('GET',ha+'/deliveries')['items']==[]
assert set(int(x['id']) for x in request('GET',ha+'/sources')['items'])==set(live_sources)
assert int(request('GET',ha+'/sync-preview')['total'])==2 and int(request('GET',ha+'/sync-preview')['history_total'])==0
for row in hcore:assert request('GET',f'/projects/{hp["id"]}/tasks/{row["id"]}')['data']['title']==row['title']
assert all((vault/name).is_file() and hashlib.sha256((vault/name).read_bytes()).hexdigest()==value for name,value in local_files.items())
(verification/'history-discard-report.json').write_text(json.dumps({'official_paca':'0.18.6','six_orphaned_legacy_links_verified':True,'access_failure_preserves_link':True,'transient_failure_preserves_link':True,'current_two_tasks_and_links_preserved':True,'single_event_cleanup_ui':True,'batch_event_cleanup_ui':True,'single_source_cleanup_ui':True,'batch_source_cleanup_ui':True,'live_change_confirmation':True,'cancel_preserves_queue':True,'associated_queue_cancelled':True,'deleted_task_queue_cancelled':True,'leased_work_protected':True,'worker_concurrency_protected':True,'valid_links_cannot_clear':True,'refresh_and_host_worker_restart':True,'replacement_delivery_replay_suppressed':True,'local_notes_and_photo_preserved':True,'connection_scope':True},ensure_ascii=False,indent=2))
