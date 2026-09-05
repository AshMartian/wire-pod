#!/usr/bin/env python3
"""Native release promotion on the Pi; builds and SSH stay on the workstation."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import pwd
import re
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path('/opt/wire-pod')
STATE = Path('/var/lib/wire-pod')
BACKUPS = Path('/var/backups/wire-pod')
SERVICE = 'wire-pod.service'
DIRECTORIES = (
    'certs', 'stt', 'vosk', 'whisper.cpp', 'vector-cloud/build',
    'chipper/jdocs', 'chipper/plugins', 'chipper/session-certs',
)
FILES = (
    'chipper/apiConfig.json', 'chipper/botConfig.json',
    'chipper/customIntents.json', 'chipper/pico.key', 'chipper/source.sh',
)


def full_sha(value):
    if not re.fullmatch(r'[0-9a-f]{40}', value):
        raise argparse.ArgumentTypeError('source SHA must be 40 lowercase hex characters')
    return value


def checksum(value):
    if not re.fullmatch(r'[0-9a-f]{64}', value):
        raise argparse.ArgumentTypeError('SHA-256 must be 64 lowercase hex characters')
    return value


def run(*args):
    subprocess.run(args, check=True)


def verify_archive(archive, expected_digest, source_sha, destination):
    """Verify the same opened archive we extract; reject links and special files."""
    with archive.open('rb') as raw:
        digest = hashlib.file_digest(raw, 'sha256').hexdigest()
        if digest != expected_digest:
            raise ValueError('artifact SHA-256 mismatch')
        raw.seek(0)
        with tarfile.open(fileobj=raw, mode='r:*') as bundle:
            members = bundle.getmembers()
            if len(members) > 20000 or sum(m.size for m in members) > 2 * 1024**3:
                raise ValueError('artifact exceeds release size limits')
            seen = set()
            for member in members:
                name = PurePosixPath(member.name)
                if name.is_absolute() or '..' in name.parts or '\\' in member.name:
                    raise ValueError(f'unsafe archive path: {member.name}')
                if not (member.isdir() or member.isfile()):
                    raise ValueError(f'artifact must contain ordinary files/directories: {member.name}')
                if name == PurePosixPath('.') and member.isdir():
                    continue
                if str(name) in seen:
                    raise ValueError(f'duplicate archive path: {member.name}')
                seen.add(str(name))
            # Extract manually: never preserve archived owners, links, or special modes.
            for member in members:
                target = destination / member.name
                if member.isdir():
                    target.mkdir(parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    with bundle.extractfile(member) as src, target.open('xb') as out:
                        while chunk := src.read(1024 * 1024):
                            out.write(chunk)
                    target.chmod(0o555 if member.mode & 0o111 else 0o444)
    manifest = json.loads((destination / 'release.json').read_text())
    if not isinstance(manifest, dict) or manifest.get('source_sha') != source_sha or manifest.get('target') != 'linux/arm64':
        raise ValueError('release.json must match source SHA and target linux/arm64')
    for required in ('chipper/chipper', 'lib/libvosk.so', 'chipper/stttest.pcm'):
        if not (destination / required).is_file():
            raise ValueError(f'artifact missing {required}')
    for required in ('chipper/webroot', 'chipper/epod', 'chipper/intent-data'):
        if not (destination / required).is_dir():
            raise ValueError(f'artifact missing {required}')
    if not os.access(destination / 'chipper/chipper', os.X_OK):
        raise ValueError('chipper binary is not executable')


def prepare_state(release):
    account = pwd.getpwnam('wirepod')
    for relative in DIRECTORIES:
        target = STATE / relative
        target.mkdir(parents=True, exist_ok=True)
        os.chown(target, account.pw_uid, account.pw_gid)
        target.chmod(0o750)
    sdk_dir = STATE / '.anki_vector'
    sdk_dir.mkdir(exist_ok=True)
    os.chown(sdk_dir, account.pw_uid, account.pw_gid)
    sdk_dir.chmod(0o750)
    for relative in (*DIRECTORIES, *FILES):
        link = release / relative
        if link.exists() or link.is_symlink():
            raise ValueError(f'artifact must not contain mutable state: {relative}')
        link.parent.mkdir(parents=True, exist_ok=True)
        link.symlink_to(STATE / relative)
    # Do not create setup markers, empty JSON, or an empty source.sh.
    if (STATE / 'chipper/useepod').is_file():
        (release / 'chipper/useepod').symlink_to(STATE / 'chipper/useepod')
    # Intermediate persistent directories must also be writable by the service.
    for relative in ('chipper', 'vector-cloud'):
        target = STATE / relative
        os.chown(target, account.pw_uid, account.pw_gid)
        target.chmod(0o750)


def release_path(source_sha):
    full_sha(source_sha)
    path = ROOT / 'releases' / source_sha
    if path.is_symlink() or not path.is_dir():
        raise ValueError(f'not an installed release: {source_sha}')
    manifest = json.loads((path / 'release.json').read_text())
    if manifest.get('source_sha') != source_sha:
        raise ValueError('installed release SHA does not match its directory')
    if not (path / '.artifact-sha256').is_file():
        raise ValueError('installed release lacks artifact verification marker')
    return path


def current_release():
    current = ROOT / 'current'
    if not current.is_symlink():
        if current.exists():
            raise ValueError('current must be a managed symlink')
        return None
    target = current.resolve(strict=True)
    if target.parent != ROOT / 'releases':
        raise ValueError('current points outside managed releases')
    return release_path(target.name)


def activate(path):
    temporary = ROOT / f'.current-{os.getpid()}'
    temporary.symlink_to(path)
    os.replace(temporary, ROOT / 'current')


def get_health(base_url, endpoint):
    request = urllib.request.Request(base_url + endpoint, headers={'Accept': 'application/json'})
    try:
        response = urllib.request.urlopen(request, timeout=3)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = json.loads(response.read(65536))
        if not isinstance(data, dict):
            raise ValueError('health response must be a JSON object')
        return response.code, data


def smoke(source_sha, allow_unconfigured=False, timeout=90, base_url='http://127.0.0.1:8080'):
    deadline = time.monotonic() + timeout
    last_error = 'service not reachable'
    while True:
        try:
            live_code, live = get_health(base_url, '/health/live')
            ready_code, ready = get_health(base_url, '/health/ready')
            if live_code != 200 or live.get('source_sha') != source_sha:
                raise ValueError('liveness/installed source SHA mismatch')
            if ready.get('source_sha') != source_sha:
                raise ValueError('readiness source SHA mismatch')
            if ready_code == 200 and ready.get('status') == 'ready':
                print(f'Ready: {source_sha}', flush=True)
                return 'ready'
            if allow_unconfigured and ready_code == 503 and ready.get('status') == 'setup_required':
                print(f'Setup required: {source_sha}; service is live, robot readiness NOT established.', flush=True)
                return 'setup_required'
            last_error = f'readiness HTTP {ready_code}, status={ready.get("status")}'
        except (OSError, ValueError) as error:
            last_error = str(error)
        if time.monotonic() >= deadline:
            raise RuntimeError(f'smoke check failed: {last_error}')
        time.sleep(2)


def snapshot(source_sha):
    stamp = time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())
    backup = BACKUPS / f'{stamp}-{source_sha}-{os.getpid()}.tar.gz'
    run('tar', '--one-file-system', '--acls', '--xattrs', '-czf', str(backup), '-C', str(STATE), '.')
    backup.chmod(0o600)
    print(f'State snapshot: {backup}', flush=True)
    return backup


def promote(target, args):
    previous = current_release()
    was_active = subprocess.run(['systemctl', 'is-active', '--quiet', SERVICE]).returncode == 0
    run('systemctl', 'stop', SERVICE)
    try:
        backup = snapshot(previous.name if previous else 'initial')
    except Exception:
        if was_active:
            run('systemctl', 'start', SERVICE)
        raise
    try:
        activate(target)
        run('systemctl', 'start', SERVICE)
        status = smoke(target.name, args.allow_unconfigured, args.timeout, args.base_url)
        run('systemctl', 'enable', SERVICE)
    except Exception:
        run('systemctl', 'stop', SERVICE)
        if previous:
            activate(previous)
            if was_active:
                run('systemctl', 'start', SERVICE)
                try:
                    smoke(previous.name, True, args.timeout, args.base_url)
                except Exception as recovery_error:
                    print(f'Previous release failed health check: {recovery_error}', file=sys.stderr)
        else:
            (ROOT / 'current').unlink(missing_ok=True)
        print(f'Promotion failed; previous executable selection restored. State was NOT restored; backup: {backup}', file=sys.stderr)
        raise
    record = {'source_sha': target.name, 'previous_sha': previous.name if previous else None,
              'state_backup': str(backup), 'status': status, 'time_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
    record_path = BACKUPS / f'promotion-{time.time_ns()}.json'
    record_path.write_text(json.dumps(record, indent=2) + '\n')
    record_path.chmod(0o600)
    print(f'Promotion record: {record_path}', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest='command', required=True)
    for command in ('deploy', 'rollback', 'smoke'):
        sub = subparsers.add_parser(command)
        if command == 'deploy':
            sub.add_argument('archive', type=Path)
            sub.add_argument('sha256', type=checksum)
        sub.add_argument('source_sha', type=full_sha)
        sub.add_argument('--allow-unconfigured', action='store_true')
        sub.add_argument('--timeout', type=int, default=90)
        sub.add_argument('--base-url', default='http://127.0.0.1:8080')
    args = parser.parse_args()
    if args.timeout < 0:
        parser.error('timeout must be non-negative')
    if args.command == 'smoke':
        smoke(args.source_sha, args.allow_unconfigured, args.timeout, args.base_url.rstrip('/'))
        return
    if os.geteuid() != 0 or os.uname().machine != 'aarch64':
        parser.error('deploy/rollback require root on an aarch64 Pi')
    if not ROOT.is_dir() or not STATE.is_dir() or not BACKUPS.is_dir():
        parser.error('run pi-bootstrap.sh first')
    with (ROOT / '.deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.command == 'deploy':
            target = ROOT / 'releases' / args.source_sha
            if target.exists() or target.is_symlink():
                target = release_path(args.source_sha)
                if (target / '.artifact-sha256').read_text().strip() != args.sha256:
                    raise ValueError('same source SHA already installed with a different artifact')
                with args.archive.open('rb') as artifact:
                    if hashlib.file_digest(artifact, 'sha256').hexdigest() != args.sha256:
                        raise ValueError('artifact SHA-256 mismatch')
            else:
                with tempfile.TemporaryDirectory(prefix='.install-', dir=ROOT / 'releases') as temp:
                    staged = Path(temp) / 'release'
                    staged.mkdir()
                    verify_archive(args.archive, args.sha256, args.source_sha, staged)
                    prepare_state(staged)
                    (staged / '.artifact-sha256').write_text(args.sha256 + '\n')
                    (staged / '.artifact-sha256').chmod(0o444)
                    for directory, _, _ in os.walk(staged, followlinks=False):
                        Path(directory).chmod(0o555)
                    staged.rename(target)
        else:
            target = release_path(args.source_sha)
        promote(target, args)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print(f'ERROR: {error}', file=sys.stderr)
        sys.exit(1)
