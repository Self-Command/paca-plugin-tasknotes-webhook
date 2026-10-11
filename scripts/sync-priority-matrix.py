"""Delayed queue and three preference modes, against the real Paca runtime."""
def priority_sql(statement):
    return subprocess.check_output(['docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-At','-v','ON_ERROR_STOP=1','-c',statement],text=True).strip()

def priority_hold():
    name='priority-order-'+str(uuid.uuid4())
    process=subprocess.Popen(['docker','exec','-e','PGAPPNAME='+name,'paca-ci-db','psql','-U','postgres','-d','paca','-At','-v','ON_ERROR_STOP=1','-c',f"SELECT pg_advisory_lock(hashtextextended('{sync_connection_id}',0));SELECT pg_sleep(90)"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for _ in range(100):
        if priority_sql(f"SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name='{name}' AND l.locktype='advisory' AND l.granted)")=='t':return name,process
        time.sleep(.05)
    raise AssertionError('Could not hold the independent queue lock')

def priority_release(held):
    name,process=held
    priority_sql(f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='{name}'")
    process.wait(timeout=10)

matrix=[]
for prefer in ('latest','server','local'):
    # Server changed first; the later received local operation either wins its
    # field or yields that field, without dropping either party's other edits.
    core=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'矩阵原始 '+prefer,'status_id':sync_mapping['open']},201)['data']
    original=sync_item(core['id'])
    request('PATCH',f'/projects/{sync_project["id"]}/tasks/{core["id"]}',{'title':'矩阵服务器先改','tags':['服务器独立标签']})
    op=preferred(original,{'title':'矩阵本地后交','priority':'high'},prefer)
    wait_operation(op)
    actual=request('GET',f'/projects/{sync_project["id"]}/tasks/{core["id"]}')['data']
    assert actual['title']==('矩阵服务器先改' if prefer=='server' else '矩阵本地后交')
    assert actual['importance']==75 and '服务器独立标签' in actual['tags']
    sync_request('POST','/operations',op) # Exact replay returns its saved result.
    assert request('GET',f'/projects/{sync_project["id"]}/tasks/{core["id"]}')['data']['title']==actual['title']
    # Local request is accepted while the worker is held. A later Paca update
    # must win ordinary mode even though the older local request executes last.
    base_item=sync_item(core['id'])
    held=priority_hold()
    try:
        delayed=preferred(base_item,{'title':'较早接收的本地请求','priority':'low'},prefer)
        request('PATCH',f'/projects/{sync_project["id"]}/tasks/{core["id"]}',{'title':'较晚接收的服务器修改','tags':['服务器后来标签']})
    finally:priority_release(held)
    wait_operation(delayed)
    actual=request('GET',f'/projects/{sync_project["id"]}/tasks/{core["id"]}')['data']
    assert actual['title']==('较早接收的本地请求' if prefer=='local' else '较晚接收的服务器修改'),(prefer,actual)
    assert actual['importance']==25 and '服务器后来标签' in actual['tags']
    # Several already-received local edits retain their own order. The native
    # event from the first write cannot suppress the second queued write.
    base_item=sync_item(core['id'])
    held=priority_hold()
    try:
        first=preferred(base_item,{'title':'连续修改 B'},prefer)
        second_item={**base_item,'snapshot':{**base_item['snapshot'],'title':'连续修改 B'}}
        second=preferred(second_item,{'title':'连续修改 C','priority':'high'},prefer)
        deleted_item={**second_item,'snapshot':{**second_item['snapshot'],'title':'连续修改 C','priority':'high'}}
        deletion=preferred(deleted_item,{},prefer,'delete')
    finally:priority_release(held)
    wait_operation(first);wait_operation(second);wait_operation(deletion)
    request('GET',f'/projects/{sync_project["id"]}/tasks/{core["id"]}',expected=404)
    # A later native deletion overrides an older queued edit only in ordinary
    # and server priority. Local priority restores the same logical identity.
    core=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'排队时删除 '+prefer,'status_id':sync_mapping['open']},201)['data']
    base_item=sync_item(core['id'])
    held=priority_hold()
    try:
        delayed=preferred(base_item,{'title':'删除前已接收的请求'},prefer)
        request('DELETE',f'/projects/{sync_project["id"]}/tasks/{core["id"]}')
    finally:priority_release(held)
    result=wait_operation(delayed,'applied' if prefer=='local' else 'superseded')
    if prefer=='local':
        assert result['result']['sync_id']==base_item['sync_id']
        assert request('GET',f'/projects/{sync_project["id"]}/tasks/{result["result"]["task_id"]}')['data']['title']=='删除前已接收的请求'
    else:request('GET',f'/projects/{sync_project["id"]}/tasks/{core["id"]}',expected=404)
    matrix.append({'mode':prefer,'both_receive_orders':True,'unrelated_fields_preserved':True,'exact_replay':True,'queued_edits_then_delete':True,'later_native_delete':True})

(verification/'sync-priority-matrix-report.json').write_text(json.dumps({'cases':matrix,'all_passed':True,'runtime':'official Paca','client_clocks_used':False},ensure_ascii=False,indent=2))
