"""Reproduce the alternate recurrence editor time-write path on the real host."""
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
probe_calls=[]
class FreezeProbe(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        payload=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
        if self.path=='/internal/v1/times/freeze':
            assert self.headers.get('Authorization')=='Bearer '+('a'*64)
            probe_calls.append(payload)
            value={'enabled':True,'frozen':payload.get('task_id')==guard_task}
        elif self.path=='/internal/v1/writeback-match':value={'matched':False}
        else:raise AssertionError('Freeze check attempted to materialize check-in: '+self.path)
        raw=json.dumps(value).encode();self.send_response(200);self.end_headers();self.wfile.write(raw)
new_task=request('POST',f'/projects/{sync_project["id"]}/tasks',{'title':'普通任务时间冻结入口','status_id':sync_mapping['open']},201)['data']
guard_task=new_task['id'];guard_item=sync_item(guard_task)
# Before freezing, saving a rule without time fields must preserve the existing times.
initial=submit(guard_item,{'scheduled':start+'T15:00:00','due':start+'T16:00:00'})
wait_operation(initial);guard_item=sync_item(guard_task)
probe=ThreadingHTTPServer(('127.0.0.1',18183),FreezeProbe)
threading.Thread(target=probe.serve_forever,daemon=True).start()
sync_process.terminate();sync_process.wait(timeout=10)
sync_env={**sync_env,'CHECKIN_WORKER_URL':'http://127.0.0.1:18183','CHECKIN_SERVICE_SECRET':'a'*64}
sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
try:
    for _ in range(100):
        try:sync_request('GET','/info');break
        except OSError:time.sleep(.2)
    with sync_playwright() as pw:
        browser=pw.chromium.launch();context=browser.new_context(viewport={'width':390,'height':844})
        assert context.request.post('http://127.0.0.1:18081/api/v1/auth/login',data={'username':'admin','password':new_password}).ok
        page=context.new_page();page.goto(f'http://127.0.0.1:18081/projects/{sync_project["id"]}/tasks/{guard_task}',wait_until='domcontentloaded')
        message=page.get_by_text('此任务为一次性任务，不会自动重复。',exact=True);expect(message).to_be_visible(timeout=30000)
        page.get_by_role('button',name='设置重复计划',exact=True).click()
        expect(page.get_by_label('首次开始时间',exact=True)).to_have_count(0)
        expect(page.get_by_label('首次结束时间',exact=True)).to_have_count(0)
        page.screenshot(path=str(verification/'ordinary-repeat-ui.png'),full_page=True)
        context.close();browser.close()
    old_core=request('GET',f'/projects/{sync_project["id"]}/tasks/{guard_task}')['data']
    alternate={'connection_id':sync_connection_id,'revision':guard_item['revision'],'op_id':str(uuid.uuid4()),'recurrence':'','recurrence_anchor':'scheduled','scheduled':start+'T15:30:00','due':start+'T16:00:00'}
    route=f'/plugins/{plugin_id}/projects/{sync_project["id"]}/tasks/{guard_task}/recurrence'
    request('PUT',route,alternate,202)
    result=wait_operation({'op_id':'paca-rule:'+alternate['op_id']},state='conflict')
    assert result['result']['status_code']==409,result
    current=request('GET',f'/projects/{sync_project["id"]}/tasks/{guard_task}')['data']
    assert current['custom_fields']==old_core['custom_fields'],'frozen alternate editor wrote a new time'
    assert not current['custom_fields']['_integration_state_v1']['recurring']
    assert any(p.get('task_id')==guard_task for p in probe_calls),'alternate editor skipped freeze authority'
    (verification/'schedule-freeze-report.json').write_text(json.dumps({'ordinary_task_not_recurring':True,'alternate_time_write_conflict':True,'frozen_core_unchanged':True,'readonly_freeze_probe':True}))
finally:
    sync_process.terminate();sync_process.wait(timeout=10)
    probe.shutdown();probe.server_close()
    sync_env.pop('CHECKIN_WORKER_URL',None);sync_env.pop('CHECKIN_SERVICE_SECRET',None)
    sync_process=subprocess.Popen(['/tmp/tasknotes-worker'],env=sync_env,stdout=sync_log,stderr=sync_log)
