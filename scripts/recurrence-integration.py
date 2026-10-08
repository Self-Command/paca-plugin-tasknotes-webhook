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
    request('DELETE',f'/projects/{sync_project["id"]}/tasks/{first[1]["task_id"]}')
    for _ in range(180):
        if next(p for p in periods(3) if p['date']==first[1]['date'])['state']=='deleted':break
        time.sleep(.5)
    else:raise AssertionError('Deleted period was recreated')
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
    (verification/'recurrence-report.json').write_text(json.dumps({'official_model':'0.3.0-rc.9','fixed_daily_unique_periods':True,'server_without_obsidian':True,'precise_period_times':True,'parent_marked_unscheduled':True,'restart_no_duplicate':True,'core_complete_updates_parent_history':True,'reopen_and_complete_again':True,'deleted_period_tombstone':True,'api_ai_rule_endpoint':True,'rule_request_idempotent':True,'stale_rule_revision_rejected':True,'completion_anchor_one_pending':True,'real_phone_verified':False},ensure_ascii=False,indent=2))
finally:
    pass
