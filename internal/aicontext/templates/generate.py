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
MAX_OMITTED = MAX_FILES
OMIT_TOO_LARGE = 'too_large'
OMIT_OVER_BUDGET = 'over_budget'
OMIT_FLAGGED = 'flagged'


class GenerationRefusal(ValueError):
    pass


class OversizedText(GenerationRefusal):
    """A text file over MAX_FILE. Raised without the path so that source_blob
    stays free of repository text; build() names the file."""


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
        raise OversizedText('A source file exceeds the context size limit; add an exclusion')
    return content, text


def mib(size):
    return '%.1f MiB' % (size / (1 << 20))


def human(size):
    if size < 1 << 10:
        return '%d B' % size
    return '%.1f KiB' % (size / (1 << 10)) if size < 1 << 20 else mib(size)


def within_budget(candidates):
    """Split {path: bytes} into what fits MAX_FILES and MAX_SOURCE and what does not.

    The largest files go first, ties by path, so the same commit always loses the
    same files and the most files survive: one big generated file should not cost
    a repository its hundreds of small ones."""
    kept = dict(candidates)
    total = sum(kept.values())
    dropped = []
    for name, size in sorted(candidates.items(), key=lambda item: (-item[1], item[0])):
        if len(kept) <= MAX_FILES and total <= MAX_SOURCE:
            break
        del kept[name]
        total -= size
        dropped.append(name)
    return kept, dropped


REASONS = {
    OMIT_TOO_LARGE: 'over the %s limit' % mib(MAX_FILE),
    OMIT_OVER_BUDGET: 'dropped to fit the size and file-count limits',
    OMIT_FLAGGED: 'withheld by the secret scan',
}


def report_omitted(omitted):
    """Say, in the run log and as an annotation, what the context does not carry.

    Only paths, sizes and fixed reasons are printed, never content. A reader of
    the context is told the same thing, so this is for the operator who would
    rather exclude the file, or fix what the scan flagged, than live with it."""
    if not omitted:
        return
    def escape(text):
        return text.replace('%', '%25').replace('\r', '%0D').replace('\n', '%0A')
    entries = ['%s (%s, %s)' % (o['path'], human(o['bytes']), REASONS[o['reason']]) for o in omitted]
    shown = entries[:10]
    if len(entries) > 10:
        shown.append('and %d more' % (len(entries) - 10))
    print('::warning title=Zoomies AI Context omitted %d file%s::%s' % (
        len(omitted), '' if len(omitted) == 1 else 's',
        escape('Listed, but their content is not in the context: ' + '; '.join(shown) +
               '. Assistants are told each file exists and why it is missing. Add an exclusion to drop it from the list.')))
    for entry in entries:
        print('omitted: ' + entry)


def build(root, output, cli, expected, identity, config_hash, source_commit):
    if not re.fullmatch('[0-9a-f]{40}', source_commit):
        raise GenerationRefusal('A pinned source commit is required')
    if command('git', '-C', str(root), 'rev-parse', 'HEAD').decode().strip() != source_commit:
        raise GenerationRefusal('Checkout does not match the source commit')
    raw = command('git', '-C', str(root), 'show', source_commit + ':zoomies-ai-context.config.json')
    if len(raw) > 512 << 10 or json.loads(raw) != expected:
        raise GenerationRefusal('Managed configuration changed; review a setup repair')
    entries = command('git', '-C', str(root), 'ls-tree', '-rz', '--full-tree', source_commit).split(b'\0')
    candidates = {}
    omitted = []
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
        try:
            source = source_blob(root, blob, size)
        except OversizedText:
            omitted.append({'path': name, 'bytes': size, 'reason': OMIT_TOO_LARGE})
            continue
        if source is not None:
            # Only the size is kept: holding every file's text until the budget
            # is known would make memory depend on the repository.
            candidates[name] = (blob, size)
    kept, dropped = within_budget({name: size for name, (blob, size) in candidates.items()})
    for name in dropped:
        omitted.append({'path': name, 'bytes': candidates[name][1], 'reason': OMIT_OVER_BUDGET})
    originals = {}
    with tempfile.TemporaryDirectory(prefix='zoomies-context-') as tmp:
        stage = Path(tmp) / 'source'
        stage.mkdir()
        for name in sorted(kept):
            content, text = source_blob(root, candidates[name][0], candidates[name][1])
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
        # then retain Git's exact original bytes and line numbers. A file it
        # removed is one its security scan identified: that is listed as omitted
        # rather than carried, never published and never allowed to pass as
        # absent. Anything it added or altered is not Git's source, so refuse.
        js_whitespace = '\u0009\u000b\u000c\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u202f\u205f\u3000\ufeff\n\r\u2028\u2029'
        trimmed = {p: c.strip(js_whitespace) for p, c in originals.items()}
        if any(trimmed.get(p) != c for p, c in generated.items()):
            raise GenerationRefusal('Repomix added or changed source; inspect exclusions and secret scanning')
        for name in sorted(set(originals) - set(generated)):
            omitted.append({'path': name, 'bytes': candidates[name][1], 'reason': OMIT_FLAGGED})
            del originals[name]
        if not originals:
            raise GenerationRefusal('No eligible source files remain')

        def encode():
            snapshot = {'manifest': {
                'schema_version': 1, 'repository': identity,
                'source_branch': expected['source_branch'], 'source_commit': source_commit,
                'config_hash': config_hash, 'generated_at': generated_at,
                'manager': 'zoomies', 'generator': 'repomix@1.18.1',
            }, 'files': [{'path': p, 'sha256': hashlib.sha256(c.encode()).hexdigest(), 'content': c}
                        for p, c in sorted(originals.items())]}
            if omitted:
                snapshot['omitted'] = sorted(omitted, key=lambda o: o['path'])
            return json.dumps(snapshot, ensure_ascii=False, separators=(',', ':')).encode()

        generated_at = datetime.datetime.now(datetime.timezone.utc).isoformat()
        encoded = encode()
        # JSON escaping can push text that fits MAX_SOURCE past MAX_SNAPSHOT, so
        # shed the largest carried file until the serialised snapshot fits.
        shed = 0
        while len(encoded) > MAX_SNAPSHOT:
            shed += 1
            if shed > 200 or len(originals) == 1:
                raise GenerationRefusal('Serialized snapshot exceeds its limit; add exclusions')
            name = max(originals, key=lambda p: (len(originals[p]), p))
            omitted.append({'path': name, 'bytes': candidates[name][1], 'reason': OMIT_OVER_BUDGET})
            del originals[name]
            encoded = encode()
        if len(omitted) > MAX_OMITTED:
            raise GenerationRefusal('%d files would be omitted and only %d can be listed; add exclusions' % (len(omitted), MAX_OMITTED))
        output.mkdir(parents=True, exist_ok=False)
        (output / 'snapshot.json').write_bytes(encoded)
        (output / 'NOTICE.md').write_text('Managed by Zoomies AI Context. Generated by Repomix. Do not edit.\n')
        (output / 'manifest.json').write_text(json.dumps({
            'manager': 'zoomies', 'template_version': 1, 'generator': 'repomix@1.18.1',
            'source_commit': source_commit, 'config_hash': config_hash,
            'snapshot_sha256': hashlib.sha256(encoded).hexdigest(),
            'token_count_method': 'not measured',
        }, indent=2) + '\n')
    report_omitted(sorted(omitted, key=lambda o: o['path']))


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
