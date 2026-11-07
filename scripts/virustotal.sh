#!/usr/bin/env bash
# Sends files to VirusTotal and prints what the antivirus engines say.
# Informational: it never fails a release, and without VT_API_KEY it does nothing.
# Usage: virustotal.sh <file>...
# VT_API replaces the API address (for tests); VT_GAP is the pause in seconds
# between API calls (a free key allows 4 calls a minute); VT_WAIT is how many
# seconds to wait for the verdicts.
set -uo pipefail

api=${VT_API:-https://www.virustotal.com/api/v3}
gap=${VT_GAP:-16}
wait_max=${VT_WAIT:-1500}
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}
max_size=33554432 # larger files need a different upload address

if [[ -z ${VT_API_KEY:-} ]]; then
  echo "VT_API_KEY is not set: skipping VirusTotal."
  exit 0
fi
if [[ $# -eq 0 ]]; then
  echo "usage: virustotal.sh <file>..." >&2
  exit 2
fi
for tool in curl jq; do
  command -v "$tool" >/dev/null || { echo "$tool is needed: skipping VirusTotal." >&2; exit 0; }
done

hash_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

last=0
body=""
code=000
# vt <curl args...>: one API call, spaced by VT_GAP, one retry after a rate-limit answer.
# The answer is left in $body and the status in $code.
vt() {
  local out now pause
  for _ in 1 2; do
    now=$(date +%s)
    pause=$((last + gap - now))
    if ((pause > 0)); then sleep "$pause"; fi
    out=$(mktemp)
    code=$(curl -sS --max-time 300 -o "$out" -w '%{http_code}' -H "x-apikey: $VT_API_KEY" "$@" 2>/dev/null) || code=000
    last=$(date +%s)
    body=$(cat "$out")
    rm -f "$out"
    if [[ $code != 429 ]]; then return 0; fi
    echo "rate limited, waiting a minute" >&2
    sleep 60
    last=$(date +%s)
  done
}

names=()
hashes=()
ids=()
for f in "$@"; do
  [[ -f $f ]] || continue
  name=$(basename "$f")
  h=$(hash_of "$f")
  names+=("$name")
  hashes+=("$h")
  vt "$api/files/$h"
  if [[ $code == 200 ]]; then
    echo "$name: VirusTotal already knows this file"
    ids+=("")
    continue
  fi
  if (($(wc -c <"$f") > max_size)); then
    echo "$name: larger than 32 MB, not uploaded"
    ids+=("")
    continue
  fi
  vt "$api/files" -F "file=@$f"
  if [[ $code != 200 ]]; then
    echo "$name: upload failed (HTTP $code)"
    ids+=("")
    continue
  fi
  ids+=("$(jq -r '.data.id // ""' <<<"$body")")
  echo "$name: uploaded"
done

# Wait for the analyses; a file VirusTotal already knew has its report ready.
deadline=$(($(date +%s) + wait_max))
reports=()
for i in "${!names[@]}"; do
  if [[ -z ${ids[i]} ]]; then
    vt "$api/files/${hashes[i]}"
    reports+=("$body")
    continue
  fi
  while :; do
    vt "$api/analyses/${ids[i]}"
    status=$(jq -r '.data.attributes.status // "?"' <<<"$body")
    if [[ $status == completed ]] || (($(date +%s) > deadline)); then break; fi
  done
  reports+=("$body")
done

{
  echo "## VirusTotal"
  echo
  echo "| file | flagged | link |"
  echo "|---|---|---|"
  for i in "${!names[@]}"; do
    r=${reports[i]}
    flagged=$(jq -r '(.data.attributes.stats // .data.attributes.last_analysis_stats // {}) as $s
      | if ($s | length) == 0 then "no verdict yet"
        else "\(($s.malicious // 0) + ($s.suspicious // 0)) of \(($s.malicious // 0) + ($s.suspicious // 0) + ($s.undetected // 0) + ($s.harmless // 0))" end' <<<"$r" 2>/dev/null || echo "no verdict yet")
    echo "| ${names[i]} | $flagged | [report](https://www.virustotal.com/gui/file/${hashes[i]}) |"
  done
  for i in "${!names[@]}"; do
    engines=$(jq -r '(.data.attributes.results // .data.attributes.last_analysis_results // {})
      | to_entries[] | select(.value.category == "malicious" or .value.category == "suspicious")
      | "\(.key): \(.value.result // .value.category)"' <<<"${reports[i]}" 2>/dev/null)
    if [[ -n $engines ]]; then
      echo
      echo "**${names[i]}** is flagged by:"
      echo
      echo '```'
      echo "$engines"
      echo '```'
    fi
  done
} >>"$summary"
exit 0
