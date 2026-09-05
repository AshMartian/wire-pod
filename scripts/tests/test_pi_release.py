"""Local contract tests; no systemd, root, robot, or network access needed."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('pi_release', Path(__file__).parents[1] / 'pi_release.py')
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
SHA = 'a' * 40


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.archive = self.root / 'release.tar.gz'
        self.destination = self.root / 'installed'
        self.destination.mkdir()

    def bundle(self, extra=None, target='linux/arm64'):
        with tarfile.open(self.archive, 'w:gz') as bundle:
            files = {
                'release.json': json.dumps({'source_sha': SHA, 'target': target}).encode(),
                'chipper/chipper': b'example binary',
                'chipper/stttest.pcm': b'audio fixture',
                'lib/libvosk.so': b'example library',
            }
            for name, content in files.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(content)
                entry.mode = 0o755 if name == 'chipper/chipper' else 0o644
                bundle.addfile(entry, io.BytesIO(content))
            for name in ('chipper/webroot', 'chipper/epod', 'chipper/intent-data'):
                entry = tarfile.TarInfo(name)
                entry.type = tarfile.DIRTYPE
                bundle.addfile(entry)
            if extra:
                bundle.addfile(extra, io.BytesIO(b''))
        return hashlib.sha256(self.archive.read_bytes()).hexdigest()

    def test_valid_bundle(self):
        digest = self.bundle()
        release.verify_archive(self.archive, digest, SHA, self.destination)
        self.assertEqual((self.destination / 'chipper/chipper').stat().st_mode & 0o777, 0o555)

    def test_checksum_before_extraction(self):
        self.bundle()
        with self.assertRaisesRegex(ValueError, 'SHA-256'):
            release.verify_archive(self.archive, '0' * 64, SHA, self.destination)
        self.assertEqual(list(self.destination.iterdir()), [])

    def test_traversal_rejected_before_extraction(self):
        digest = self.bundle(tarfile.TarInfo('../outside'))
        with self.assertRaisesRegex(ValueError, 'unsafe archive path'):
            release.verify_archive(self.archive, digest, SHA, self.destination)
        self.assertFalse((self.root / 'outside').exists())
        self.assertEqual(list(self.destination.iterdir()), [])

    def test_symlink_rejected(self):
        link = tarfile.TarInfo('chipper/link')
        link.type = tarfile.SYMTYPE
        link.linkname = '/etc/passwd'
        digest = self.bundle(link)
        with self.assertRaisesRegex(ValueError, 'ordinary files'):
            release.verify_archive(self.archive, digest, SHA, self.destination)

    def test_wrong_target_rejected(self):
        digest = self.bundle(target='linux/amd64')
        with self.assertRaisesRegex(ValueError, 'target linux/arm64'):
            release.verify_archive(self.archive, digest, SHA, self.destination)


class HealthTests(unittest.TestCase):
    def probe(self, status, *, allow=False, ready_sha=SHA):
        responses = [(200, {'source_sha': SHA}),
                     (200 if status == 'ready' else 503, {'source_sha': ready_sha, 'status': status})]
        with patch.object(release, 'get_health', side_effect=responses):
            return release.smoke(SHA, allow_unconfigured=allow, timeout=0)

    def test_ready(self):
        self.assertEqual(self.probe('ready'), 'ready')

    def test_setup_requires_explicit_option(self):
        with self.assertRaises(RuntimeError):
            self.probe('setup_required')
        self.assertEqual(self.probe('setup_required', allow=True), 'setup_required')

    def test_unconfigured_option_does_not_hide_stt_failure(self):
        with self.assertRaises(RuntimeError):
            self.probe('speech_unavailable', allow=True)

    def test_wrong_running_commit_fails(self):
        with self.assertRaisesRegex(RuntimeError, 'source SHA mismatch'):
            self.probe('ready', ready_sha='b' * 40)


class PromotionTests(unittest.TestCase):
    def test_failed_health_restores_previous_executable(self):
        previous = Path('/opt/wire-pod/releases') / ('b' * 40)
        target = Path('/opt/wire-pod/releases') / SHA
        args = type('Options', (), {'allow_unconfigured': False, 'timeout': 0,
                                  'base_url': 'http://127.0.0.1:8080'})()
        with patch.object(release, 'current_release', return_value=previous), \
             patch.object(release.subprocess, 'run') as systemctl, \
             patch.object(release, 'run') as run, \
             patch.object(release, 'snapshot', return_value=Path('/var/backups/wire-pod/test.tar.gz')), \
             patch.object(release, 'activate') as activate, \
             patch.object(release, 'smoke', side_effect=[RuntimeError('unhealthy'), 'ready']):
            systemctl.return_value.returncode = 0
            with self.assertRaisesRegex(RuntimeError, 'unhealthy'):
                release.promote(target, args)
            self.assertEqual([call.args[0] for call in activate.call_args_list], [target, previous])
            self.assertNotIn(('systemctl', 'enable', release.SERVICE), [call.args for call in run.call_args_list])


if __name__ == '__main__':
    unittest.main()
