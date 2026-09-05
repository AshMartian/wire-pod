#!/usr/bin/env bash
set -euo pipefail

[[ ${EUID} -eq 0 ]] || { echo 'Run bootstrap as root on the production Pi.' >&2; exit 1; }
[[ $(uname -m) == aarch64 ]] || { echo 'This release requires an aarch64 Pi OS.' >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(dirname -- "$script_dir")
unit_path=/etc/systemd/system/wire-pod.service
if [[ -e "$unit_path" ]] && ! grep -q '^# Managed by wire-pod scripts/pi-bootstrap.sh$' "$unit_path"; then
    echo "Refusing to replace unmanaged $unit_path; inspect the existing installation first." >&2
    exit 1
fi
apt-get update
apt-get install -y --no-install-recommends python3 ca-certificates libopus0 libopusfile0 libatomic1 libgomp1 libstdc++6
if ! id wirepod >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /var/lib/wire-pod --shell /usr/sbin/nologin wirepod
fi
install -d -o root -g root -m 0755 /opt/wire-pod /opt/wire-pod/releases
install -d -o wirepod -g wirepod -m 0750 /var/lib/wire-pod
install -d -o root -g root -m 0700 /var/backups/wire-pod
install -d -o root -g wirepod -m 0750 /etc/wire-pod
if [[ ! -e /etc/wire-pod/wire-pod.env ]]; then
    install -o root -g wirepod -m 0640 "$repo_dir/deploy/wire-pod.env" /etc/wire-pod/wire-pod.env
fi
install -o root -g root -m 0644 "$repo_dir/deploy/wire-pod.service" "$unit_path"
systemctl daemon-reload
echo 'Bootstrap complete. No service started or network settings changed.'
echo 'Stage the verified Vosk en-US model in /var/lib/wire-pod/vosk/models/en-US/model, then deploy an artifact.'
