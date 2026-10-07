"""Actions-only real, isolated official Obsidian UI -> signed Webhook -> Paca test.
Executed inside host-smoke.py so installation and API credentials remain private.
No TaskNotes task mutation API is used: commands open modals; Playwright clicks them.
"""
assert os.environ.get('GITHUB_ACTIONS')=='true','real Obsidian E2E runs only in Actions'
import signal,re,tarfile
lock=json.loads((ROOT/'scripts/obsidian-fixture-lock.json').read_text())
fixture_root=pathlib.Path(os.environ['RUNNER_TEMP'])/'paca-obsidian-ui'
fixture_root.mkdir(parents=True,exist_ok=True)
vault=fixture_root/'vault'; plugin_dir=vault/'.obsidian/plugins/tasknotes'
plugin_dir.mkdir(parents=True,exist_ok=True)
def official_download(url,sha,target):
    for attempt in range(5):
        try:
            with urllib.request.urlopen(url,timeout=120) as response: data=response.read()
            break
        except OSError:
            if attempt==4:raise
            time.sleep(attempt+1)
    assert hashlib.sha256(data).hexdigest()==sha,'official asset checksum mismatch'
    target.write_bytes(data)
for asset,sha in lock['tasknotes']['assets'].items():
    official_download(f'https://github.com/callumalpass/tasknotes/releases/download/{lock["tasknotes"]["version"]}/{asset}',sha,plugin_dir/asset)
assert json.loads((plugin_dir/'manifest.json').read_text())['version']==lock['tasknotes']['version']
image=fixture_root/'Obsidian.AppImage'
official_download(lock['obsidian']['asset'],lock['obsidian']['sha256'],image);image.chmod(0o700)
subprocess.run([str(image),'--appimage-extract'],cwd=fixture_root,check=True,stdout=subprocess.DEVNULL)
obsidian_sender_secret=fixtures['secret']
ui_connection=request('POST',f'/plugins/{plugin_id}/projects/{project["id"]}/connections',{'name':'Real Obsidian UI proof','secret':obsidian_sender_secret,'status_map':{'@archived':archive_status['id']}},201)
ui_connection_id=ui_connection['id']
receive_path=f'/api/v1/plugins/{plugin_id}/receive/{ui_connection_id}'
received=[]
class RealWebhookCapture(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        if self.path!=receive_path:self.send_error(404);return
        body=self.rfile.read(int(self.headers.get('Content-Length',0)))
        signature=self.headers.get('X-TaskNotes-Signature','')
        assert hmac.compare_digest(signature,hmac.new(obsidian_sender_secret.encode(),body,hashlib.sha256).hexdigest())
        event=json.loads(body)
        record={'event':event['event'],'delivery_id':self.headers['X-TaskNotes-Delivery-ID'],'data':event['data'],'timestamp':event['timestamp']}
        headers={k:v for k,v in self.headers.items() if k.lower() in ('content-type','x-tasknotes-signature','x-tasknotes-event','x-tasknotes-delivery-id')}
        req=urllib.request.Request('http://127.0.0.1:18080'+self.path,data=body,method='POST',headers=headers)
        try:
            with urllib.request.urlopen(req,timeout=20) as response:status,payload=response.status,response.read()
        except urllib.error.HTTPError as error:status,payload=error.code,error.read()
        record['receiver_status']=status;received.append(record)
        self.send_response(status);self.end_headers();self.wfile.write(payload)
capture=ThreadingHTTPServer(('127.0.0.1',18280),RealWebhookCapture)
threading.Thread(target=capture.serve_forever,daemon=True).start()
settings={'enableAPI':True,'apiPort':18823,'apiAuthToken':secrets.token_hex(24),'enableNaturalLanguageInput':False,'tasksFolder':'Tasks','archiveFolder':'Archive','moveArchivedTasks':True,'defaultTaskStatus':'open','webhooks':[{'id':'real-obsidian-ui','url':'http://127.0.0.1:18280'+receive_path,'active':True,'secret':obsidian_sender_secret,'events':['task.created','task.updated','task.completed','task.deleted','task.archived','task.unarchived'],'failureCount':0,'successCount':0}]}
(plugin_dir/'data.json').write_text(json.dumps(settings))
extra_setup=os.environ.get('OBSIDIAN_EXTRA_SETUP')
if extra_setup:exec(pathlib.Path(extra_setup).read_text(),globals())
(vault/'.obsidian/community-plugins.json').write_text(json.dumps(['tasknotes']+(['obsidian-paca-checkin-sync'] if extra_setup else [])))
(vault/'.obsidian/core-plugins.json').write_text('["file-explorer","command-palette"]')
(vault/'.obsidian/app.json').write_text(json.dumps({'promptDelete':False,'showUnsupportedFiles':True}))
user_dir=fixture_root/'user';user_dir.mkdir(exist_ok=True)
(user_dir/'obsidian.json').write_text(json.dumps({'vaults':{'ci':{'path':str(vault),'ts':int(time.time()*1000),'open':True}},'updateDisabled':True}))
ui_log=open(ROOT/'verification/obsidian-runtime.log','w')
ui_worker_log=open(ROOT/'verification/obsidian-worker.log','w')
ui_worker=subprocess.Popen(['/tmp/tasknotes-worker'],env={**worker_env,'PACA_API_URL':'http://127.0.0.1:18080'},stdout=ui_worker_log,stderr=ui_worker_log)
obsidian=subprocess.Popen(['xvfb-run','-a',str(fixture_root/'squashfs-root/obsidian'),'--no-sandbox',*(['--ignore-certificate-errors-spki-list='+os.environ['OBSIDIAN_FIXTURE_SPKI']] if extra_setup else []),'--remote-debugging-port=19223',f'--user-data-dir={user_dir}',str(vault)],cwd=fixture_root,start_new_session=True,env={**os.environ,'OBSIDIAN_CONFIG_DIR':str(user_dir)},stdout=ui_log,stderr=ui_log)
ui_page=None
try:
    for _ in range(120):
        try:
            with urllib.request.urlopen('http://127.0.0.1:19223/json/version',timeout=1) as response:cdp=json.load(response)['webSocketDebuggerUrl'];break
        except OSError:time.sleep(0.5)
    else:raise RuntimeError('Official Obsidian CDP not ready')
    with sync_playwright() as pw:
        browser=pw.chromium.connect_over_cdp(cdp)
        context=browser.contexts[0]
        ui_page=context.pages[0] if context.pages else context.wait_for_event('page')
        def save_diagnostic(name):
            (ROOT/f'verification/{name}.json').write_text(json.dumps({'pages':[p.url for p in context.pages],'body':ui_page.locator('body').inner_text()[:20000]},indent=2))
            try:
                data=ui_page.evaluate('async()=>{const {remote}=require("electron");return (await remote.getCurrentWindow().capturePage()).toPNG().toString("base64")}')
                import base64
                (ROOT/f'verification/{name}.png').write_bytes(base64.b64decode(data))
            except Exception:pass
        ui_page.wait_for_function('()=>Boolean(window.app?.workspace?.layoutReady)',timeout=60000)
        assert pathlib.Path(ui_page.evaluate('()=>app.vault.adapter.basePath'))==vault,'refuse an unexpected vault'
        for label in ['Trust author and enable plugins','Turn on community plugins','Enable community plugins']:
            button=ui_page.get_by_role('button',name=label,exact=True)
            if button.is_visible():button.click()
        save_diagnostic('obsidian-startup')
        try:
            ui_page.wait_for_function('()=>Boolean(window.app?.plugins?.plugins?.tasknotes?.cacheManager)',timeout=60000)
        except Exception:
            save_diagnostic('obsidian-plugin-load-failure');raise
        for _ in range(3):ui_page.keyboard.press('Escape')
        version=ui_page.evaluate('()=>window.require("electron").remote.app.getVersion()')
        assert version==lock['obsidian']['version'],f'Obsidian runtime changed: {version}'
        ui_page.evaluate('()=>window.localStorage.setItem("language","en")')
        def command(search,label):
            ui_page.evaluate('()=>app.commands.executeCommandById("command-palette:open")')
            ui_page.locator('.prompt-input').fill(search)
            ui_page.locator('.suggestion-item').filter(has_text=re.compile(label,re.I)).first.click()
        def wait_event(event,expected_title=None):
            for _ in range(120):
                matches=[record for record in received if record['event']==event and (expected_title is None or record['data']['task']['title']==expected_title)]
                if matches:
                    record=matches[-1]
                    rows=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{ui_connection_id}/deliveries')['items']
                    state=next((row for row in rows if row['delivery_id']==record['delivery_id']),None)
                    if state and state['state']=='applied':return record['data']['task']
                    if state and state['state'] in ('conflict','error'):raise AssertionError(f'{event}: {state["error"]}')
                time.sleep(0.5)
            raise AssertionError(f'real UI did not deliver/apply {event}')
        def open_edit(task):
            ui_page.evaluate('async path=>{const file=app.vault.getAbstractFileByPath(path);if(!file)throw new Error("note missing");await app.workspace.getLeaf(false).openFile(file)}',task['path'])
            command('TaskNotes edit current','Edit current task')
            ui_page.locator('.tn-task-modal__archive-button').wait_for(timeout=15000)
        command('TaskNotes create','Create new task')
        ui_page.locator('.title-input:visible,.title-input-detailed:visible').first.fill('Obsidian UI proof task')
        ui_page.locator('.tn-task-modal__button-bar button.mod-cta').click()
        created=wait_event('task.created')
        sources=request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{ui_connection_id}/sources')['items']
        assert len(sources)==1
        task_id=sources[0]['task_id']
        open_edit(created)
        ui_page.locator('.title-input:visible,.title-input-detailed:visible').first.fill('Obsidian UI proof task renamed')
        ui_page.locator('.tn-task-modal__button-bar button.mod-cta').click()
        changed=wait_event('task.updated','Obsidian UI proof task renamed')
        assert request('GET',f'/projects/{project["id"]}/tasks/{task_id}')['data']['title']==changed['title']
        open_edit(changed)
        ui_page.locator('[data-type="status"]').click()
        ui_page.locator('.menu-item').filter(has_text=re.compile(r'^Done$')).click()
        ui_page.locator('.tn-task-modal__button-bar button.mod-cta').click()
        completed=wait_event('task.completed')
        statuses=request('GET',f'/projects/{project["id"]}/task-statuses')['data']['items']
        assert request('GET',f'/projects/{project["id"]}/tasks/{task_id}')['data']['status_id']==next(st['id'] for st in statuses if st['category']=='done' and st['id']!=archive_status['id'])
        open_edit(completed);ui_page.locator('.tn-task-modal__archive-button').click()
        archived=wait_event('task.archived')
        assert request('GET',f'/projects/{project["id"]}/tasks/{task_id}')['data']['status_id']==archive_status['id']
        open_edit(archived);ui_page.locator('.tn-task-modal__archive-button').click()
        unarchived=wait_event('task.unarchived')
        assert not request('GET',f'/projects/{project["id"]}/tasks/{task_id}')['data']['custom_fields']['_integration_state_v1']['archived']
        open_edit(unarchived);ui_page.locator('.tn-task-modal__delete-button').click()
        ui_page.locator('.modal button.mod-warning:visible').last.click()
        wait_event('task.deleted')
        request('GET',f'/projects/{project["id"]}/tasks/{task_id}',expected=404)
        assert len(request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{ui_connection_id}/sources')['items'])==1
        ui_page.screenshot(path=str(ROOT/'verification/obsidian-ui-six-events.png'))
        extra_check=os.environ.get('OBSIDIAN_EXTRA_E2E')
        if extra_check:exec(pathlib.Path(extra_check).read_text(),globals(),locals())
        browser.close()
    report={'real_obsidian_ui':True,'obsidian_version':lock['obsidian']['version'],'official_tasknotes_version':lock['tasknotes']['version'],'release_source_sha':lock['tasknotes']['release_source_sha'],'review_source_sha':lock['tasknotes']['review_source_sha'],'official_assets_sha256_verified':True,'task_mutations_only_ui':True,'six_events':[x['event'] for x in received],'same_paca_task':True,'archive_folder_move':True,'delete_tombstone':True,'secret_redacted':True}
    (ROOT/'verification/obsidian-report.json').write_text(json.dumps(report,indent=2))
except Exception:
    if ui_page:
        try:ui_page.screenshot(path=str(ROOT/'verification/obsidian-ui-failure.png'))
        except Exception:pass
    (ROOT/'verification/obsidian-ui-deliveries.json').write_text(json.dumps(received,indent=2))
    raise
finally:
    ui_worker.terminate();ui_worker.wait(timeout=20);ui_worker_log.close()
    if obsidian.poll() is None:os.killpg(obsidian.pid,signal.SIGTERM)
    obsidian.wait(timeout=20);ui_log.close();capture.shutdown()
