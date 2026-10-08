"""Real core/API recurrence verification; executed only by GitHub Actions."""
from datetime import datetime,timedelta
from zoneinfo import ZoneInfo

try:
    for _ in range(60):
        try: sync_request('GET','/info');break
        except OSError:time.sleep(.3)
    cfg=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections')['items'] if c['id']==sync_connection_id)
    request('PUT',sync_admin+'/sync-config',{'mode':'enabled','revision':cfg['revision'],'recurrence_enabled':True})
    local=datetime.now(ZoneInfo('Asia/Shanghai'))
    start=local.date().isoformat()
    series_id=str(uuid.uuid4())
    created={'op_id':str(uuid.uuid4()),'sync_id':series_id,'base_revision':0,'kind':'create','base':{},'changes':{'title':'循环阅读','status':'open','priority':'high','scheduled':start+'T09:00:00','due':start+'T10:00:00','details':'## 每期内容\n- 阅读一章','tags':['学习'],'archived':False,'recurrence':'FREQ=DAILY;COUNT=3','recurrence_anchor':'scheduled'}}
    sync_request('POST','/operations',created,202)
    result=wait_operation(created)
    series_task=result['result']['task_id']
    recurrence_route=f'/plugins/{plugin_id}/projects/{sync_project["id"]}/tasks/{series_task}/recurrence'
    def periods(count,timeout=140):
        for _ in range(timeout):
            info=request('GET',recurrence_route)['items'][0]
            items=info['periods']
            if len(items)==count and all(p['task_id'] for p in items):return items
            assert not info['error'],info['error']
            time.sleep(.5)
        raise AssertionError(info)
    first=periods(3)
    assert len({p['task_id'] for p in first})==3
    for p in first:
        period_core=request('GET',f'/projects/{sync_project["id"]}/tasks/{p["task_id"]}')['data']
        extra=period_core['custom_fields']['_task_sync_v1']
        assert extra['recurrence_parent']==series_id and extra['occurrence_date']==p['date']
        assert not period_core['custom_fields']['_integration_state_v1']['recurring']
        assert p['date'] in period_core['custom_fields']['_integration_state_v1']['start_source']
    # The official materialization initially inherits its mother's managed marker.
    # Series/date identity must take precedence before D rewrites the child's marker.
    period_item=sync_item(first[0]['task_id'])
    note={**period_item['snapshot'],'id':'Tasks/本期阅读.md','path':'Tasks/本期阅读.md','dateCreated':datetime.now(ZoneInfo('UTC')).isoformat(),'dateModified':datetime.now(ZoneInfo('UTC')).isoformat(),'details':'<!-- paca-sync-id:'+series_id+' -->\n<!-- paca-task-content:start -->\n'+period_item['snapshot']['details']+'\n<!-- paca-task-content:end -->'}
    webhook={'event':'task.created','timestamp':datetime.now(ZoneInfo('UTC')).isoformat(),'vault':{'name':'Official recurring fixture','path':'/fixture'},'data':{'task':note}}
    raw=json.dumps(webhook,ensure_ascii=False,separators=(',',':')).encode()
    delivery='inherited-parent-marker-'+str(uuid.uuid4())
    req=urllib.request.Request(base+'/plugins/'+plugin_id+'/receive/'+sync_connection_id,data=raw,method='POST',headers={'Content-Type':'application/json','X-TaskNotes-Event':'task.created','X-TaskNotes-Delivery-ID':delivery,'X-TaskNotes-Signature':hmac.new(sync_sender_secret.encode(),raw,hashlib.sha256).hexdigest()})
    with urllib.request.urlopen(req,timeout=20) as response:assert response.status==202
    for _ in range(120):
        received=next(row for row in request('GET',sync_admin+'/deliveries')['items'] if row['delivery_id']==delivery)
        if received['state']=='applied':break
        assert received['state'] not in ('error','conflict','uncertain'),received
        time.sleep(.5)
    else:raise AssertionError('Materialized child remained unsynchronized')
    matched=[row for row in request('GET',sync_admin+'/sources')['items'] if row.get('path',row.get('source_key'))==note['path']]
    assert len(matched)==1 and matched[0]['task_id']==first[0]['task_id'],'Inherited marker rebound the child to its parent'
    # No Obsidian process is running while the server creates these future tasks.
    sync_process.terminate();sync_process.wait(timeout=10)
    sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
    for _ in range(60):
        try:
            sync_request('GET','/info')
            after=periods(3);break
        except OSError:time.sleep(.3)
    assert [p['task_id'] for p in after]==[p['task_id'] for p in first],'Restart duplicated periods'
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{first[0]["task_id"]}',{'status_id':sync_mapping['done']})
    for _ in range(180):
        parent=sync_item(series_task)
        if first[0]['date'] in (parent['snapshot'].get('complete_instances') or []):break
        time.sleep(.5)
    else:raise AssertionError('Server completion did not update parent history')
    # Reopening a completed period must remove only that date from the parent history.
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{first[0]["task_id"]}',{'status_id':sync_mapping['open']})
    for _ in range(180):
        reopened=sync_item(series_task)
        if first[0]['date'] not in (reopened['snapshot'].get('complete_instances') or []):break
        time.sleep(.5)
    else:raise AssertionError('Reopened period retained parent completion')
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{first[0]["task_id"]}',{'status_id':sync_mapping['done']})
    for _ in range(180):
        repeated=sync_item(series_task)
        if first[0]['date'] in (repeated['snapshot'].get('complete_instances') or []):break
        time.sleep(.5)
    else:raise AssertionError('A second completion reused a stale progress operation')
    for skip in (True,False,True,False):
        parent=sync_item(series_task)
        skipped_date=first[2]['date']
        skip_op=submit(parent,{'skipped_instances':[skipped_date] if skip else []})
        wait_operation(skip_op)
        for _ in range(180):
            native_period=request('GET',f'/projects/{sync_project["id"]}/tasks/{first[2]["task_id"]}')['data']
            state=next(p for p in periods(3) if p['date']==skipped_date)['state']
            if (native_period['status_id']==sync_mapping['@archived'])==skip and state==('skipped' if skip else 'planned'):break
            time.sleep(.5)
        else:raise AssertionError({'skip':skip,'state':state,'native':native_period['status_id']})
    request('DELETE',f'/projects/{sync_project["id"]}/tasks/{first[1]["task_id"]}')
    for _ in range(180):
        if next(p for p in periods(3) if p['date']==first[1]['date'])['state']=='deleted':break
        time.sleep(.5)
    else:raise AssertionError('Deleted period was recreated')
    stop_rule=submit(sync_item(series_task),{'recurrence':None,'recurrence_anchor':None})
    wait_operation(stop_rule)
    parent_core=request('GET',f'/projects/{sync_project["id"]}/tasks/{series_task}')['data']
    assert parent_core['custom_fields']['_integration_state_v1']['recurring'],'Stopped parent became a normal reminder'
    for _ in range(180):
        if next(p for p in periods(3) if p['date']==first[2]['date'])['state']=='cancelled':break
        time.sleep(.5)
    else:raise AssertionError('Stopping the series retained an unstarted period')
    # A native/API/AI task can become completion-anchored using the same scoped rule endpoint.
    parent_native=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'完成后再安排','status_id':sync_mapping['open']},201)['data']
    item=sync_item(parent_native['id'])
    route=f'/plugins/{plugin_id}/projects/{sync_project["id"]}/tasks/{parent_native["id"]}/recurrence'
    rule_request={'connection_id':sync_connection_id,'revision':item['revision'],'op_id':str(uuid.uuid4()),'recurrence':'FREQ=DAILY','recurrence_anchor':'completion','scheduled':start+'T11:00:00','due':start+'T12:00:00'}
    request('PUT',route,rule_request,202)
    for _ in range(180):
        info=request('GET',route)['items'][0]
        if len(info['periods'])==1 and info['periods'][0]['task_id']:break
        time.sleep(.5)
    else:raise AssertionError(info)
    assert info['snapshot']['recurrence_anchor']=='completion'
    request('PUT',route,rule_request,202)
    request('PUT',route,{**rule_request,'recurrence':'FREQ=WEEKLY'},409)
    request('PUT',route,{**rule_request,'op_id':str(uuid.uuid4())},409)
    # Existing TaskNotes periods imported before enabling the scheduler keep their task IDs.
    config_now=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections')['items'] if c['id']==sync_connection_id)
    request('PUT',sync_admin+'/sync-config',{'mode':'enabled','revision':config_now['revision'],'recurrence_enabled':False})
    migrated_series=str(uuid.uuid4())
    future_day=(local.date()+timedelta(days=2)).isoformat()
    migrate_op={'op_id':str(uuid.uuid4()),'sync_id':migrated_series,'base_revision':0,'kind':'create','base':{},'changes':{'title':'已有周期升级','status':'open','priority':'normal','scheduled':future_day+'T09:00:00','due':future_day+'T10:00:00','recurrence':'FREQ=DAILY;COUNT=2','recurrence_anchor':'scheduled'}}
    sync_request('POST','/operations',migrate_op,202)
    migrated_parent=wait_operation(migrate_op)['result']['task_id']
    old_period=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'已有周期笔记','status_id':sync_mapping['open'],'custom_fields':{'_task_sync_v1':{'recurrence_parent':migrated_series,'occurrence_date':future_day}}},201)['data']
    old_item=sync_item(old_period['id'])
    assert old_item['kind']=='occurrence'
    config_now=next(c for c in request('GET',f'/plugins/{plugin_id}/projects/{sync_project["id"]}/connections')['items'] if c['id']==sync_connection_id)
    request('PUT',sync_admin+'/sync-config',{'mode':'enabled','revision':config_now['revision'],'recurrence_enabled':True})
    migrate_route=f'/plugins/{plugin_id}/projects/{sync_project["id"]}/tasks/{migrated_parent}/recurrence'
    for _ in range(360):
        migration=request('GET',migrate_route)['items'][0]
        assert not migration['error'],migration
        if len(migration['periods'])==2 and all(p['task_id'] for p in migration['periods']):break
        time.sleep(.5)
    else:raise AssertionError(migration)
    assert next(p['task_id'] for p in migration['periods'] if p['date']==future_day)==old_period['id'],'Upgrade duplicated an existing occurrence'
    # A manually materialized historical note remains synchronizable without a historical batch.
    historical=(local.date()-timedelta(days=1)).isoformat()
    historical_id=str(uuid.uuid4())
    historical_op={'op_id':str(uuid.uuid4()),'sync_id':historical_id,'base_revision':0,'kind':'create','base':{},'changes':{'title':'历史周期笔记','status':'open','priority':'normal','recurrence_parent':migrated_series,'occurrence_date':historical}}
    sync_request('POST','/operations',historical_op,202)
    historical_result=wait_operation(historical_op)
    for _ in range(120):
        historical_series=request('GET',migrate_route)['items'][0]
        if any(p['date']==historical and p['task_id']==historical_result['result']['task_id'] for p in historical_series['periods']):break
        time.sleep(.5)
    else:raise AssertionError(historical_series)
    assert len(historical_series['periods'])==3,'An explicit historical note backfilled extra periods'
    (verification/'recurrence-report.json').write_text(json.dumps({'official_model':'0.3.0-rc.9','fixed_daily_unique_periods':True,'server_without_obsidian':True,'precise_period_times':True,'parent_marked_unscheduled':True,'restart_no_duplicate':True,'core_complete_updates_parent_history':True,'reopen_and_complete_again':True,'repeat_skip_unskip':True,'deleted_period_tombstone':True,'stopped_series_does_not_remind_parent':True,'api_ai_rule_endpoint':True,'rule_request_idempotent':True,'stale_rule_revision_rejected':True,'completion_anchor_one_pending':True,'existing_period_upgrade_preserved':True,'explicit_historical_period_no_batch':True,'inherited_parent_marker_ignored':True,'real_phone_verified':False},ensure_ascii=False,indent=2))
finally:
    pass
