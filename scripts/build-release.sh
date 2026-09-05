#!/usr/bin/env bash
# Build only committed source, on the development workstation.
set -euo pipefail
repo_dir=$(git -C "$(dirname -- "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
source_sha=$(git -C "$repo_dir" rev-parse --verify "${1:-HEAD}^{commit}")
output_dir=${2:-"$repo_dir/artifacts"}
mkdir -p -- "$output_dir"
output_dir=$(cd -- "$output_dir" && pwd)
archive="$output_dir/wire-pod-$source_sha-linux-arm64.tar.gz"
[[ ! -e "$archive" ]] || { echo "Artifact already exists: $archive" >&2; exit 1; }
task_dir=$(mktemp -d -t wire-pod-build.XXXXXXXX)
trap 'rm -r -- "$task_dir"' EXIT
mkdir "$task_dir/source" "$task_dir/release"
git -C "$repo_dir" archive "$source_sha" | tar -x -C "$task_dir/source"
[[ -f "$task_dir/source/build/Dockerfile.release" ]] || {
    echo 'The chosen commit must include build/Dockerfile.release.' >&2; exit 1;
}
docker build --file "$task_dir/source/build/Dockerfile.release" \
    --target artifact --build-arg "SOURCE_SHA=$source_sha" \
    --output "type=local,dest=$task_dir/release" "$task_dir/source"
source_epoch=$(git -C "$repo_dir" show -s --format=%ct "$source_sha")
tar --sort=name --mtime="@$source_epoch" --owner=0 --group=0 --numeric-owner \
    --dereference -C "$task_dir/release" -cf - . | gzip -n > "$archive"
(cd "$output_dir" && sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256")
printf 'Artifact: %s\nSource: %s\n' "$archive" "$source_sha"
sha256sum "$archive"
