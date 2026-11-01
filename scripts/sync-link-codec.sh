#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
source_dir="${PASSAGE_LINK_DIR:-$root/../passage-link}"
cp "$source_dir"/link/*.go "$root/core/third_party/passage-link/link/"
cp "$source_dir/LICENSE" "$root/core/third_party/passage-link/LICENSE"
