#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
source_dir="${PASSAGE_LINK_DIR:-$root/../passage-link}"
target_dir="$root/core/third_party/passage-link/link"
for source in "$source_dir"/link/*.go; do
  case "$(basename "$source")" in
    compat07_test.go|format08_test.go) continue ;;
  esac
  cp "$source" "$target_dir/"
done
rm -f "$target_dir/compat07_test.go" "$target_dir/format08_test.go"
mkdir -p "$target_dir/testdata"
cp "$source_dir/link/testdata/sub-v1.json" "$source_dir/link/testdata/sub-v1-closed.json" "$target_dir/testdata/"
cp "$source_dir/LICENSE" "$root/core/third_party/passage-link/LICENSE"
