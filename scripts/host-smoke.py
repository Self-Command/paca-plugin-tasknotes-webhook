import hashlib, hmac, http.cookiejar, json, os, pathlib, secrets, subprocess, time, urllib.request, urllib.error

ROOT=pathlib.Path(__file__).resolve().parent.parent
source_manifest=json.loads((ROOT/'plugin.json').read_text())
manifest=json.loads((ROOT/f'release/wasm/{source_manifest["id"]}/plugin.json').read_text())
plugin_id=manifest['id']
exec((ROOT/'scripts/upgrade-prepare.py').read_text(),globals())
password=secrets.token_urlsafe(24)
new_password=secrets.token_urlsafe(24)

def cmd(*args):
    subprocess.run(args,check=True,stdout=subprocess.DEVNULL)

cmd('docker','network','create','paca-ci')
cmd('docker','run','-d','--name','paca-ci-db','--network','paca-ci','-e','POSTGRES_PASSWORD=ci-only-password','-e','POSTGRES_DB=paca','postgres:16-alpine')
cmd('docker','run','-d','--name','paca-ci-cache','--network','paca-ci','valkey/valkey:8-alpine')
for _ in range(50):
    if subprocess.run(['docker','exec','paca-ci-db','pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0: break
    time.sleep(1)
env={'DATABASE_URL':'postgres://postgres:ci-only-password@paca-ci-db:5432/paca?sslmode=disable','REDIS_URL':'redis://paca-ci-cache:6379','JWT_SECRET':secrets.token_hex(32),'ADMIN_USERNAME':'admin','ADMIN_PASSWORD':password,'ENCRYPTION_KEY':secrets.token_hex(32),'PUBLIC_URL':'http://localhost:18080','COOKIE_SECURE':'false','PLUGINS_WASM_DIR':'/plugins/wasm','PLUGINS_FRONTEND_DIR':'/plugins/frontend','STORAGE_PROVIDER':'s3','STORAGE_ENDPOINT':'http://paca-ci-storage:9000','STORAGE_ACCESS_KEY_ID':'ci-access-key','STORAGE_SECRET_ACCESS_KEY':'ci-secret-key','AI_AGENT_INTERNAL_KEY':secrets.token_hex(32),'STORAGE_BUCKET':'paca','STORAGE_REGION':'us-east-1'}
args=['docker','run','-d','--name','paca-ci-api','--network','paca-ci','--network-alias','api','-p','127.0.0.1:18080:8080','-v',f'{ROOT}/release:/plugins']
for k,v in env.items(): args+=['-e',f'{k}={v}']
args+=['pacaai/paca-api@sha256:42b36fcb167f39bf07c04464b6d71ea49b1f7a8a745a2e9391623e76c5a17ad9']
cmd(*args)
jar=http.cookiejar.CookieJar()
opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
base='http://localhost:18080/api/v1'
def request(method,path,data=None,expected=200,headers=None):
    body=json.dumps(data).encode() if data is not None else None
    req=urllib.request.Request(base+path,data=body,method=method,headers={'Content-Type':'application/json',**(headers or {})})
    try:
        with opener.open(req,timeout=20) as r: status,payload=r.status,r.read()
    except urllib.error.HTTPError as e: status,payload=e.code,e.read()
    if status!=expected: raise RuntimeError(f'{method} {path}: {status}: {payload.decode()}')
    return json.loads(payload) if payload else {}
for _ in range(90):
    try:
        request('POST','/auth/login',{'username':'admin','password':password})
        break
    except (OSError,RuntimeError): time.sleep(2)
else: raise RuntimeError('official API did not become ready')
request('PATCH','/users/me/password',{'current_password':password,'new_password':new_password},204)
request('POST','/auth/login',{'username':'admin','password':new_password})
installed=request('POST','/admin/plugins',{'name':plugin_id,'version':legacy_manifest['version'],'manifest':legacy_manifest,'enabled':True},201)['data']
assert request('GET',f'/plugins/{plugin_id}/health')['schema_version']==1
worker_secret=request('POST',f'/plugins/{plugin_id}/admin/worker-credential',{},201)['secret']
request('PATCH',f'/admin/plugins/{installed['id']}',{'enabled':False})
cmd('docker','cp',str(current_package)+ '/.',f'paca-ci-api:/plugins/wasm/{plugin_id}/')
request('PATCH',f'/admin/plugins/{installed['id']}',{'manifest':manifest,'version':manifest['version'],'enabled':True})
health=request('GET',f'/plugins/{plugin_id}/health')
assert health['schema_version']==3 and health['id']==plugin_id
stamp=str(int(time.time()))
nonce=secrets.token_hex(24)
signature=hmac.new(worker_secret.encode(),f'GET\n/worker/control\n{stamp}\n{nonce}'.encode(),hashlib.sha256).hexdigest()
worker_headers={'X-Worker-Timestamp':stamp,'X-Worker-Nonce':nonce,'X-Worker-Signature':signature}
control=request('GET',f'/plugins/{plugin_id}/worker/control',headers=worker_headers)
assert control['enabled'] and control['schema_version']==3
request('GET',f'/plugins/{plugin_id}/worker/control',expected=409,headers=worker_headers)
request('GET',f'/plugins/{plugin_id}/worker/control',expected=401)
project=request('POST','/projects',{'name':'Plugin baseline','task_id_prefix':'CI'},201)['data']
request('GET',f'/plugins/{plugin_id}/projects/{project["id"]}/status')
task=request('POST',f'/projects/{project["id"]}/tasks',{'title':'Official task preserved'},201)['data']
request('PATCH',f'/projects/{project["id"]}/tasks/{task["id"]}',{'title':'Official update preserved'})
request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':False})
request('GET',f'/plugins/{plugin_id}/health',expected=404)
request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')
request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':True})
request('GET',f'/plugins/{plugin_id}/health')
cmd('docker','restart','paca-ci-api')
time.sleep(5)
request('GET',f'/plugins/{plugin_id}/health')
exec((ROOT/'scripts/tasknotes-integration.py').read_text(),globals())
verification=ROOT/'verification'
verification.mkdir(parents=True,exist_ok=True)
(ROOT/'ci.Caddyfile').write_text(':80 {\n handle /api/* {\n  reverse_proxy paca-ci-api:8080\n }\n handle_path /plugins/* {\n  root * /var/www/plugins\n  file_server\n }\n handle {\n  reverse_proxy paca-ci-web:3000\n }\n}\n')
cmd('docker','run','-d','--name','paca-ci-web','--network','paca-ci','pacaai/paca-web@sha256:c65dc2fa6384be8bbafdda9a220d525d54c730f63f2a0e0b4c167c7bb9452995')
cmd('docker','run','-d','--name','paca-ci-caddy','--network','paca-ci','-p','127.0.0.1:18081:80','-v',f'{ROOT}/ci.Caddyfile:/etc/caddy/Caddyfile:ro','-v',f'{ROOT}/release/frontend:/var/www/plugins:ro','caddy:2-alpine')
for _ in range(40):
    try:
        with urllib.request.urlopen('http://127.0.0.1:18081/api/healthz',timeout=2) as ready:
            if ready.status==200: break
    except OSError: time.sleep(0.5)
else: raise RuntimeError('Caddy did not become ready')
from playwright.sync_api import sync_playwright, expect
with sync_playwright() as pw:
    browser=pw.chromium.launch()
    context=browser.new_context(viewport={'width':1440,'height':1000})
    response=context.request.post('http://127.0.0.1:18081/api/v1/auth/login',data={'username':'admin','password':new_password})
    assert response.ok, 'browser login unsuccessful'
    page=context.new_page()
    page.goto(f'http://127.0.0.1:18081/projects/{project["id"]}/settings/',wait_until='domcontentloaded')
    page.get_by_role('button',name=manifest['displayName'],exact=True).last.click(timeout=45000)
    page.get_by_role('status').filter(has_text='已连接宿主').wait_for(timeout=30000)
    page.get_by_label('项目连接',exact=True).select_option(connection_id)
    field=page.get_by_label('TaskNotes 生成的 Secret',exact=True)
    assert field.get_attribute('type')=='password'
    ui_secret=secrets.token_hex(32)
    field.fill(ui_secret)
    before_ui_sources=request('GET',connection_path+'/sources')['items']
    with page.expect_response(lambda r:r.request.method=='PATCH' and r.url.endswith('/connections/'+connection_id)) as saved:
        page.get_by_role('button',name='保存连接',exact=True).click()
    assert saved.value.ok
    expect(field).to_have_value('')
    verify_saved_secret(ui_secret)
    verify_saved_secret(replacement,401)
    assert request('GET',connection_path+'/sources')['items']==before_ui_sources
    page.screenshot(path=str(verification/'plugin-settings.png'),full_page=True)
    page.goto(f'http://127.0.0.1:18081/projects/{project["id"]}/tasks/{target["id"]}',wait_until='domcontentloaded')
    page.get_by_role('heading',name='TaskNotes 来源',exact=True).wait_for(timeout=30000)
    page.screenshot(path=str(verification/'task-source.png'),full_page=True)
    browser.close()
reload_path=f'/plugins/{plugin_id}/projects/{project["id"]}/connections/{connection_id}/sources'
retained_sources=request('GET',reload_path)['items']
request('PATCH',f'/admin/plugins/{installed["id"]}',{'manifest':manifest,'version':manifest['version'],'enabled':True})
assert request('GET',f'/plugins/{plugin_id}/health')['schema_version']==3
assert request('GET',reload_path)['items']==retained_sources,'Manifest reload changed source associations'
verify_saved_secret(ui_secret)
request('DELETE',f'/admin/plugins/{installed["id"]}',expected=204)
request('GET',f'/plugins/{plugin_id}/health',expected=404)
request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')
report={'official_paca':'0.18.6','plugin':plugin_id,'migration':True,'wasm':True,'worker_hmac':True,'nonce_replay_rejected':True,'frontend_host':True,'task_crud':True,'disable_enable':True,'restart':True}
report.update({'historical_package_upgrade':True,'upgrade_source':legacy_source,'worker_credential_preserved':True,'manifest_reload_preserves_business_data':True,'manifest_reload':True,'uninstall_preserves_core_tasks':True,'source_task_panel':True})
report.update({'sender_secret_settings_ui':True,'secret_field_cleared_after_save':True,'sender_secret_survives_manifest_reload':True})
(verification/'host-report.json').write_text(json.dumps(report,indent=2))
print(json.dumps({'official_paca':'0.18.6','plugin':plugin_id,'migration':True,'wasm':True,'task_crud':True,'disable_enable':True,'restart':True}))
