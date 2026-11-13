#!/usr/bin/env bash
# Regenerate the embedded interface-service scaffold from examples/interface-service.
# 从 examples/interface-service 重新生成内嵌的 interface-service 脚手架快照。
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import pathlib, shutil

src = pathlib.Path("examples/interface-service")
dst = pathlib.Path("cmd/gofy/contract-template")
if dst.exists():
    shutil.rmtree(dst)
dst.mkdir(parents=True)
count = 0
for f in sorted(src.rglob("*")):
    if not f.is_file() or ".git" in f.parts:
        continue
    rel = f.relative_to(src)
    out = dst / (str(rel) + ".tmpl")
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(f.read_bytes())
    count += 1
print(f"synced {count} template files from {src}")
PY
