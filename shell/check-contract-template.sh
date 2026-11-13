#!/usr/bin/env bash
# Verify that the embedded scaffold snapshot matches examples/interface-service.
# 校验内嵌脚手架快照与 examples/interface-service 逐字节一致。
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import pathlib, sys

src = pathlib.Path("examples/interface-service")
dst = pathlib.Path("cmd/gofy/contract-template")
src_files = {
    str(f.relative_to(src)) + ".tmpl": f
    for f in src.rglob("*") if f.is_file() and ".git" not in f.parts
}
dst_files = {str(f.relative_to(dst)): f for f in dst.rglob("*") if f.is_file()}
missing = sorted(set(src_files) - set(dst_files))
extra = sorted(set(dst_files) - set(src_files))
changed = sorted(
    key for key in set(src_files) & set(dst_files)
    if src_files[key].read_bytes() != dst_files[key].read_bytes()
)
if missing or extra or changed:
    print("contract template snapshot is stale; run: make sync-contract-template", file=sys.stderr)
    for label, items in (("missing", missing), ("extra", extra), ("changed", changed)):
        for item in items[:10]:
            print(f"  {label}: {item}", file=sys.stderr)
    sys.exit(1)
print(f"contract template snapshot matches {len(src_files)} files")
PY
