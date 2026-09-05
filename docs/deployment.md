# Native production releases

Develop and build on the workstation. The Pi installs a verified ARM64 artifact and runs its binary directly as the `wirepod` system account. It does not build, pull Git, or download models during service startup.

## Files and release contract

| Path on the Pi | Purpose |
| --- | --- |
| `/opt/wire-pod/releases/<full-source-sha>` | Root-owned release files; read-only to the service |
| `/opt/wire-pod/current` | Atomically replaced symlink selecting the release |
| `/var/lib/wire-pod` | Configuration, enrollment, certificates, models, plugins, SDK credentials |
| `/etc/wire-pod/wire-pod.env` | Preserved systemd environment configuration |
| `/var/backups/wire-pod` | Root-only state snapshots and promotion records |
| `journalctl -u wire-pod.service` | Runtime logs; journal retention is controlled by the host |

The tar archive contains ordinary files/directories, including the stable `chipper/chipper` launcher, `chipper/chipper-vosk`, `chipper/chipper-whisper`, `chipper/stttest.pcm`, `chipper/webroot/`, `chipper/epod/`, `chipper/intent-data/`, and `lib/libvosk.so`. The root-owned `STT_SERVICE` environment selects `vosk` or the remote OpenAI-compatible `whisper` client explicitly. Dereference library symlinks when packaging. The manifest must contain `source_sha` as the full 40-character lowercase commit ID and `target` as `linux/arm64`. The archive SHA-256 is supplied independently on deployment. Other build metadata may be included in the manifest.

Exclude runtime state from the archive. Installation creates relative-layout compatibility links for `certs/`, `stt/`, `vosk/`, `whisper.cpp/`, `vector-cloud/build/`, `chipper/jdocs/`, `chipper/plugins/`, and `chipper/session-certs/`, plus optional configuration files under `chipper/`. Files behind the links are created by the application when needed. No empty JSON, `source.sh`, or `useepod` marker is invented. Existing optional `useepod` state is linked only when present. `WIREPOD_SDK_DIR` points at `/var/lib/wire-pod/.anki_vector`.

The same source SHA cannot be reinstalled with different artifact bytes. Build-input changes that affect a release should be committed, producing a new source SHA. A release that fails promotion remains installed for diagnosis; neither old releases nor backups are automatically deleted.

## Bootstrap and initial install

Copy `scripts/` and `deploy/` from the reviewed commit to a staging directory on the Pi. Run bootstrap there with an authorized privileged shell:

```bash
sudo bash scripts/pi-bootstrap.sh
```

Bootstrap installs runtime packages, the service account and unit, and initial Vosk/en-US environment settings. It preserves an existing environment file and refuses to replace an unrelated unit. It does not start the service or change networking. It requires an aarch64 OS, systemd, Debian package repositories, and root access. This is the only normal deployment step that installs OS packages; upgrades should be deliberate.

Stage a separately verified Vosk model at `/var/lib/wire-pod/vosk/models/en-US/model`. Give `wirepod` read/traverse access to the model; keep writable state owned by `wirepod`. Do not assume the language-model endpoint supplies transcription. The initial service uses Vosk; change the configured backend only with a compatible artifact and verified speech service.

Transfer the artifact and its checksum from the workstation, then run with the literal checksum and full commit ID from the build output:

```text
sudo bash scripts/pi-deploy.sh /absolute/path/to/release.tar.gz <sha256> <full-source-sha> --allow-unconfigured
```

The placeholders above must be replaced; the deployment script validates their exact formats. The installer rejects checksum mismatch, archive traversal, links/special files, wrong target architecture, and manifest/source mismatch before promotion. Initial setup is an explicit exception: `--allow-unconfigured` accepts only a live matching binary whose readiness reports `setup_required`. It does not accept `starting`, failed STT, or an incorrect running commit, and does not claim either robot works.

Complete setup and enroll the robots through the local interface. Then require full readiness:

```text
bash scripts/pi-smoke.sh <full-source-sha>
```

If the configured web port changes, pass the matching loopback URL to deploy, rollback, and smoke, for example `--base-url http://127.0.0.1:8081`. Requests have bounded timeouts. `--timeout 90` controls the overall polling interval by default; Vosk initialization may require a deliberately longer interval on a cold Pi.

## Promote, inspect, and roll back

Normal promotion omits `--allow-unconfigured`. The script serializes installers with a lock, verifies/stages the release, stops the service, snapshots persistent state, atomically switches `current`, starts the binary, and checks `/health/live` and `/health/ready` against the expected commit. A successful promotion enables reboot startup and records the prior release, backup path, and health outcome. A failed promotion selects the prior executable and restarts it if it was previously running. On the first failed installation, it removes only the newly created `current` link and leaves the service stopped.

```bash
sudo systemctl status wire-pod.service --no-pager
sudo journalctl -u wire-pod.service -n 100 --no-pager
readlink -f /opt/wire-pod/current
```

Roll back to a specific previously installed SHA:

```text
sudo bash scripts/pi-rollback.sh <previous-full-source-sha>
```

Rollback also takes a state snapshot and checks health. It switches executable versions while preserving current enrollment and configuration. Automatic failure recovery does **not** restore old state. Every change affecting state must document backward compatibility; a healthy old executable alone does not prove state compatibility.

For an incompatible state change, use the snapshot named in the promotion record. In a privileged maintenance session, stop the service, inspect the specific root-owned snapshot with `tar -tzf`, move the current `/var/lib/wire-pod` directory to a distinctly named sibling such as `/var/lib/wire-pod.before-restore-YYYYMMDDTHHMMSSZ`, recreate `/var/lib/wire-pod`, and restore the selected snapshot there using GNU tar with `--acls --xattrs`. Preserve archive ownership/modes. Keep the moved state until the restore has been validated. Then select the corresponding compatible release with `pi-rollback.sh`, run the smoke check, and verify both robots reconnect without re-pairing. Restoration is a deliberate operation because it discards newer state from the active view; the moved directory provides recovery. Do not restore a snapshot into a running service or overlay it on newer files.

## Validation boundaries

Local tooling checks:

```bash
python3 -m unittest discover -s scripts/tests -v
bash -n scripts/pi-bootstrap.sh scripts/pi-deploy.sh scripts/pi-rollback.sh scripts/pi-smoke.sh
systemd-analyze verify deploy/wire-pod.service
```

The final command may report an absent `/opt/wire-pod/current/chipper/chipper` on the workstation; that path exists only after installation. Check other diagnostics independently.

The semantic smoke test verifies the running commit and application readiness. Hardware acceptance additionally requires both ESNs connected, actual voice/STT/TTS, camera snapshot, supervised control and stop/release, reboot reconnection, and a deploy/rollback drill. Record artifact SHA-256, source SHA, Pi OS, robot firmware, checks performed, and open limitations for each promotion. Network/AP changes and continuous autonomy are separate milestones.

The service receives only the capability needed to bind low ports and writes only persistent state and its private temporary directory. Enrollment/configuration APIs retain their existing access model; this deployment does not make them safe to expose publicly. Network access policy is handled with the production network milestone.
