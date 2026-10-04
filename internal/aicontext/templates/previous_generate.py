# Managed by Zoomies AI Context. Repository text is data, never executable setup.
import codecs
import datetime
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

MAX_FILE = 1 << 20
MAX_SOURCE = 24 << 20
MAX_SNAPSHOT = 32 << 20
MAX_FILES = 5000


class GenerationRefusal(ValueError):
    pass


def command(*args, **kwargs):
    return subprocess.check_output(args, stderr=subprocess.DEVNULL, **kwargs)


def excluded(name, patterns):
    for pattern in patterns:
        if fnmatch.fnmatchcase(name, pattern):
            return True
        while pattern.startswith('**/'):
            pattern = pattern[3:]
            if fnmatch.fnmatchcase(name, pattern):
                return True
    return False


def safe_path(name):
    parts = name.split('/')
    return (name and len(name) <= 1024 and not name.startswith('/')
            and not any(c in name for c in '\\\x00\r\n:')
            and all(p not in ('', '.', '..') and p.lower() not in ('.git', 'node_modules', '.zoomies')
                    and not p.lower().startswith('.env') for p in parts)
            and not name.lower().endswith(('.pem', '.key', '.p12', '.pfx', '.db', '.sqlite', '.sqlite3')))


def source_blob(root, blob, size):
    # Bound reads even for enormous Git blobs. Detect binary data before
    # applying the text limit; brand images and PDFs are not source context.
    process = subprocess.Popen(['git', '-C', str(root), 'cat-file', 'blob', blob],
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        content = process.stdout.read(MAX_FILE + 1)
    finally:
        process.stdout.close()
        if size > MAX_FILE:
            process.terminate()
        status = process.wait()
    if size <= MAX_FILE and status != 0:
        raise RuntimeError('Git blob could not be read')
    if b'\0' in content:
        return None
    try:
        # An oversized sample can end halfway through a UTF-8 character.
        text = codecs.getincrementaldecoder('utf-8')().decode(content, final=size <= MAX_FILE)
    except UnicodeDecodeError:
        return None
    if size > MAX_FILE:
        raise GenerationRefusal('A source file exceeds the context size limit; add an exclusion')
    return content, text


def build(root, output, cli, expected, identity, config_hash, source_commit):
    if not re.fullmatch('[0-9a-f]{40}', source_commit):
        raise GenerationRefusal('A pinned source commit is required')
    if command('git', '-C', str(root), 'rev-parse', 'HEAD').decode().strip() != source_commit:
        raise GenerationRefusal('Checkout does not match the source commit')
    raw = command('git', '-C', str(root), 'show', source_commit + ':zoomies-ai-context.config.json')
    if len(raw) > 512 << 10 or json.loads(raw) != expected:
        raise GenerationRefusal('Managed configuration changed; review a setup repair')
    entries = command('git', '-C', str(root), 'ls-tree', '-rz', '--full-tree', source_commit).split(b'\0')
    originals = {}
    total = 0
    with tempfile.TemporaryDirectory(prefix='zoomies-context-') as tmp:
        stage = Path(tmp) / 'source'
        stage.mkdir()
        for entry in entries:
            if not entry:
                continue
            info, name = entry.split(b'\t', 1)
            mode, kind, blob = info.decode().split(' ')
            name = name.decode('utf-8', errors='strict')
            if mode not in ('100644', '100755') or kind != 'blob' or not safe_path(name):
                continue
            if excluded(name, expected['exclude']):
                continue
            size = int(command('git', '-C', str(root), 'cat-file', '-s', blob))
            source = source_blob(root, blob, size)
            if source is None:
                continue
            content, text = source
            total += len(content)
            if total > MAX_SOURCE or len(originals) >= MAX_FILES:
                raise GenerationRefusal('Source context exceeds its limits; add exclusions')
            originals[name] = text
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        if not originals:
            raise GenerationRefusal('No eligible source files remain')
        repomix_config = Path(tmp) / 'repomix.json'
        repomix_config.write_text(json.dumps({
            'output': {'style': 'json', 'compress': False, 'removeComments': False,
                       'removeEmptyLines': False, 'showLineNumbers': False, 'truncateBase64': False},
            'ignore': {'useGitignore': False, 'useDefaultPatterns': False, 'customPatterns': []},
            'security': {'enableSecurityCheck': True},
        }))
        pack = Path(tmp) / 'pack.json'
        subprocess.run([str(cli), str(stage), '--config', str(repomix_config), '--output', str(pack), '--quiet'],
                       cwd=tmp, check=True, timeout=300, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if pack.stat().st_size > 64 << 20:
            raise GenerationRefusal('Repomix output exceeds its limit')
        generated = json.loads(pack.read_text())['files']
        # Repomix trims file boundaries. Verify that this is its only change,
        # then retain Git's exact original bytes and line numbers.
        # Repomix removes files its security scan identifies. Fail publication
        # rather than quietly publishing an incomplete, apparently fresh pack.
        js_whitespace = '\u0009\u000b\u000c\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u202f\u205f\u3000\ufeff\n\r\u2028\u2029'
        if generated != {p: c.strip(js_whitespace) for p, c in originals.items()}:
            raise GenerationRefusal('Repomix omitted or changed source; inspect exclusions and secret scanning')
        snapshot = {'manifest': {
            'schema_version': 1, 'repository': identity,
            'source_branch': expected['source_branch'], 'source_commit': source_commit,
            'config_hash': config_hash, 'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'manager': 'zoomies', 'generator': 'repomix@1.18.1',
        }, 'files': [{'path': p, 'sha256': hashlib.sha256(c.encode()).hexdigest(), 'content': c}
                    for p, c in sorted(originals.items())]}
        encoded = json.dumps(snapshot, ensure_ascii=False, separators=(',', ':')).encode()
        if len(encoded) > MAX_SNAPSHOT:
            raise GenerationRefusal('Serialized snapshot exceeds its limit')
        output.mkdir(parents=True, exist_ok=False)
        (output / 'snapshot.json').write_bytes(encoded)
        (output / 'NOTICE.md').write_text('Managed by Zoomies AI Context. Generated by Repomix. Do not edit.\n')
        (output / 'manifest.json').write_text(json.dumps({
            'manager': 'zoomies', 'template_version': 1, 'generator': 'repomix@1.18.1',
            'source_commit': source_commit, 'config_hash': config_hash,
            'snapshot_sha256': hashlib.sha256(encoded).hexdigest(),
            'token_count_method': 'not measured',
        }, indent=2) + '\n')


if __name__ == '__main__':
    try:
        build(Path(os.environ['SOURCE_DIR']), Path(os.environ['OUTPUT_DIR']), Path(os.environ['REPOMIX_CLI']),
              json.loads(os.environ['EXPECTED_CONFIG']), json.loads(os.environ['REPOSITORY_IDENTITY']),
              os.environ['CONFIG_HASH'], os.environ['SOURCE_COMMIT'])
    except GenerationRefusal as error:
        raise SystemExit('Context generation refused: ' + str(error))
    except Exception:
        # Generator diagnostics can contain source or secrets. Actions sees
        # only a fixed refusal; the previous valid branch remains untouched.
        raise SystemExit('Context generation refused. Check source limits, exclusions, managed configuration and secret scanning.')
