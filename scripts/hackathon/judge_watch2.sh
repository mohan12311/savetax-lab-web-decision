#!/bin/bash
# 판정기 복구 감시(10분 간격, 최대 24시간). 복구되면 사용자가 승인한(10/20·10/23) 제출 전부(10/19 블록 28건 + 10/22~23 claims_new 72건)를 현재 본문으로 한 번씩 다시 올려 재검사를 받는다.
cd /Users/sa/dev/savetax-lab || exit 1
SINCE=$(date -u +%Y-%m-%dT%H:%M)
for i in $(seq 1 144); do
  make found ARGS="--max-pages 1" >/dev/null 2>&1
  r=$(python3 -I - "$SINCE" <<'PY'
import gzip, json, glob, os, collections, sys
f = sorted(glob.glob("data/bronze/contest/found/**/*.jsonl.gz", recursive=True), key=os.path.getmtime)[-1]
rows = []
for l in gzip.open(f, "rt"):
    rows += json.loads(l)["body"].get("items") or []
rows = [c for c in rows if c["kind"] == "participant" and c["found_at"] >= sys.argv[1]]
st = collections.Counter(c["status"] for c in rows)
print(("RECOVERED " if st.get("confirmed", 0) + st.get("held", 0) else "WAIT ") + str(dict(st)))
PY
)
  echo "$(date +%m-%d\ %H:%M) $r"
  case "$r" in RECOVERED*)
    echo "== 판정기 복구 — 승인된 10/19 블록 28건 재검사용 수정을 올린다"
    for id in $(cat /private/tmp/claude-501/-Users-sa-dev-savetax-lab/add911ee-8dc3-4be5-9e38-5b54d5e2e07d/scratchpad/blk28.txt); do
      echo "-- $id"; make revise ID=$id 2>&1 | grep -v '^{' | grep -E '수정|lint|오류|실패|안내' | grep -v '^  줄' | head -6
    done
    echo "== 10/22~23 제출 건 재검사용 수정"
    for id in $(cat /private/tmp/claude-501/-Users-sa-dev-savetax-lab/add911ee-8dc3-4be5-9e38-5b54d5e2e07d/scratchpad/new_sub.txt); do
      echo "-- $id"; make revise ID=../claims_new/$id 2>&1 | grep -v '^{' | grep -E '수정|lint|오류|실패|안내' | grep -v '^  줄' | head -6
    done
    exit 0;;
  esac
  sleep 600
done
echo "24시간 동안 판정기 복구 신호 없음 — 수정은 보내지 않았다"
