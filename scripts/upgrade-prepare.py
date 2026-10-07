"""Historical package upgrade fixture; executed only in GitHub Actions."""
import shutil,tarfile

def prepare_upgrade():
    assert os.environ.get('GITHUB_ACTIONS')=='true','Actions-only upgrade fixture'
    repo=os.environ['GITHUB_REPOSITORY']
    expected={
        'Self-Command/paca-plugin-tasknotes-webhook':'bf4415b52780a62b3f903c2459b332b534f57f3e',
        'Self-Command/paca-plugin-pushgo-queue':'795297b55524476012e7688522bd580176d3e1b8',
    }[repo]
    staging=ROOT/'ci-upgrade'
    staging.mkdir(exist_ok=True)
    current=staging/'current'
    shutil.copytree(ROOT/f'release/wasm/{plugin_id}',current)
    url=f'https://github.com/{repo}/releases/download/v0.1.0-dev.10/'
    def download(name):
        for attempt in range(6):
            try:
                with urllib.request.urlopen(url+name,timeout=45) as response:return response.read()
            except OSError:
                if attempt==5:raise
                time.sleep(2*(attempt+1))
    info=json.loads(download('build-info.json'))
    assert info['source_sha']==expected,'Historical source SHA mismatch'
    checks={line.split()[1].lstrip('*'):line.split()[0] for line in download('checksums.txt').decode().splitlines()}
    payload=download('plugin-install.tar.gz')
    assert hashlib.sha256(payload).hexdigest()==checks['plugin-install.tar.gz']
    package=staging/'legacy.tar.gz'
    package.write_bytes(payload)
    destination=ROOT/f'release/wasm/{plugin_id}'
    shutil.rmtree(destination)
    with tarfile.open(package) as archive:
        for member in archive.getmembers():
            if member.name.startswith(f'wasm/{plugin_id}/'):
                archive.extract(member,ROOT/'release',filter='data')
    legacy=json.loads((destination/'plugin.json').read_text())
    assert legacy['id']==plugin_id
    return legacy,current,expected
legacy_manifest,current_package,legacy_source=prepare_upgrade()
