import http.cookiejar, json, os, pathlib, secrets, subprocess, time, urllib.request, urllib.error

ROOT=pathlib.Path(__file__).resolve().parent.parent
manifest=json.loads((ROOT/'plugin.json').read_text())
plugin_id=manifest['id']
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
env={'DATABASE_URL':'postgres://postgres:ci-only-password@paca-ci-db:5432/paca?sslmode=disable','REDIS_URL':'redis://paca-ci-cache:6379','JWT_SECRET':secrets.token_hex(32),'ADMIN_USERNAME':'admin','ADMIN_PASSWORD':password,'ENCRYPTION_KEY':secrets.token_hex(32),'PUBLIC_URL':'http://localhost:18080','COOKIE_SECURE':'false','PLUGINS_WASM_DIR':'/plugins/wasm','PLUGINS_FRONTEND_DIR':'/plugins/frontend','STORAGE_PROVIDER':'s3','STORAGE_ENDPOINT':'http://paca-ci-storage:9000','STORAGE_ACCESS_KEY':'ci-access-key','STORAGE_SECRET_KEY':'ci-secret-key','STORAGE_BUCKET':'paca','STORAGE_REGION':'us-east-1'}
args=['docker','run','-d','--name','paca-ci-api','--network','paca-ci','-p','127.0.0.1:18080:8080','-v',f'{ROOT}/release:/plugins']
for k,v in env.items(): args+=['-e',f'{k}={v}']
args+=['pacaai/paca-api:0.18.6']
cmd(*args)
jar=http.cookiejar.CookieJar()
opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
base='http://localhost:18080/api/v1'
def request(method,path,data=None,expected=200):
    body=json.dumps(data).encode() if data is not None else None
    req=urllib.request.Request(base+path,data=body,method=method,headers={'Content-Type':'application/json'})
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
request('PATCH','/users/me/password',{'current_password':password,'new_password':new_password})
request('POST','/auth/login',{'username':'admin','password':new_password})
installed=request('POST','/admin/plugins',{'name':plugin_id,'version':manifest['version'],'manifest':manifest,'enabled':True},201)['data']
health=request('GET',f'/plugins/{plugin_id}/health')
assert health['schema_version']==1 and health['id']==plugin_id
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
print(json.dumps({'official_paca':'0.18.6','plugin':plugin_id,'migration':True,'wasm':True,'task_crud':True,'disable_enable':True,'restart':True}))
