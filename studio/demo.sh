#!/usr/bin/env bash
# 2부 데모의 바닥: 합성 데이터 → 가짜 대회 서버·판매처 → 1부 CLI(선점 조회 → 원문 보관 → 게이트·preview → 승인 → 제출 → lint).
# 실제 토큰·키체인·실서버를 쓰지 않는다. 1부 CLI는 데모 모드(루프백·데모 인증서·데모 토큰만)로만 부른다.
#
#   studio/demo.sh        한 번 돌리고 끝낸다(make demo)
#   studio/demo.sh serve  가짜 서버만 띄워 둔다(make demo-serve) — Ctrl-C로 끝
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO="$ROOT/studio/demo"
SYNTH="$DEMO/synth"
DATA="$DEMO/data"
TLS="$DEMO/tls"
BIN="$DEMO/bin"
LOG="$DEMO/fakeserver.log"
CONTEST_ADDR="${DEMO_CONTEST_ADDR:-127.0.0.1:18443}"
SHOP_ADDR="${DEMO_SHOP_ADDR:-127.0.0.1:18444}"
SEED="${DEMO_SEED:-7}"
TOKEN="savetax_demo_0000000000000000000000000000000000000000000" # 비밀이 아니다(fakeserver.DemoToken과 같다)
mode="${1:-run}"

# 실제 키는 이 스크립트 안으로 들이지 않는다
unset SAVETAX_TOKEN NAVER_CLIENT_ID NAVER_CLIENT_SECRET

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

build() {
  step "빌드: 가짜 서버(studio/fakeserver)·1부 CLI(collector)"
  mkdir -p "$BIN"
  (cd "$ROOT/studio/fakeserver" && go build -o "$BIN/fakeserver" .)
  (cd "$ROOT/collector" && go build -o "$BIN/savetax" ./cmd/savetax)
}

synth() {
  step "합성 데이터 만들기(seed $SEED, LLM 없음, 대회 데이터셋 안 씀)"
  (cd "$ROOT/studio/synth" && uv run --quiet python -m studio_synth --seed "$SEED" --out "$SYNTH" --shop-base "https://$SHOP_ADDR")
}

start_server() {
  step "가짜 대회 API https://$CONTEST_ADDR · 가짜 판매처 https://$SHOP_ADDR"
  mkdir -p "$TLS"
  rm -f "$TLS/ready.json"
  "$BIN/fakeserver" -data "$SYNTH" -tls-dir "$TLS" -contest-addr "$CONTEST_ADDR" -shop-addr "$SHOP_ADDR" -token "$TOKEN" 2>"$LOG" &
  SERVER_PID=$!
  trap 'kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true' EXIT
  for _ in $(seq 1 100); do
    if [ -f "$TLS/ready.json" ]; then
      echo "준비됨 (로그 $LOG)"
      return 0
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      cat "$LOG"
      echo "가짜 서버가 뜨지 못했다"
      exit 1
    fi
    sleep 0.1
  done
  cat "$LOG"
  echo "가짜 서버가 10초 안에 준비되지 않았다"
  exit 1
}

# 1부 CLI를 데모 모드로 부른다(collector/internal/config가 루프백·데모 토큰이 아니면 거절한다)
savetax() {
  env SAVETAX_DEMO=1 SAVETAX_CONTEST_BASE_URL="https://$CONTEST_ADDR" SAVETAX_DEMO_CA="$TLS/ca.pem" \
    SAVETAX_TOKEN="$TOKEN" SAVETAX_DATA_DIR="$DATA" SAVETAX_CONTEST_RPS=20 SAVETAX_RETAIL_RPS=20 \
    "$BIN/savetax" "$@"
}

# claims/index.json에서 사례 $1(어려운 사례는 시드와 무관하게 고정)의 필드 $2
pick() {
  python3 -I -c 'import json, sys
rows = [r for r in json.load(open(sys.argv[1], encoding="utf-8")) if r["case_id"] == sys.argv[2]]
print(rows[0][sys.argv[3]])' "$SYNTH/claims/index.json" "$1" "$2"
}

approve() {
  if [ -t 0 ] && [ -z "${DEMO_YES:-}" ]; then
    read -r -p "  위 preview와 근거를 보고 제출을 승인합니까? [y/N] " ans
    [ "$ans" = "y" ] || [ "$ans" = "Y" ]
  else
    echo "  (대화형 입력이 없어 승인한 것으로 진행한다 — DEMO_YES=1 또는 비대화형 실행)"
  fi
}

case "$mode" in
serve)
  build
  synth
  start_server
  step "가짜 서버를 띄워 두었다 — Ctrl-C로 끝낸다"
  cat <<EOF
1부 CLI를 직접 부르려면:
  SAVETAX_DEMO=1 SAVETAX_CONTEST_BASE_URL=https://$CONTEST_ADDR SAVETAX_DEMO_CA=$TLS/ca.pem \\
  SAVETAX_TOKEN=$TOKEN SAVETAX_DATA_DIR=$DATA $BIN/savetax lookup --id SYN0001
EOF
  wait "$SERVER_PID"
  ;;
run)
  build
  synth
  rm -rf "$DATA"
  start_server
  # hard-01: 198×198 ↔ 200×200 동급(통과) · hard-14: 동급이지만 1.196배(기준 미달)
  PASS_ID="$(pick hard-01 item_id)"
  PASS_URL="$(pick hard-01 page_url)"
  PASS_NEED="$(pick hard-01 need)"
  GAP_ID="$(pick hard-14 item_id)"
  GAP_URL="$(pick hard-14 page_url)"
  GAP_NEED="$(pick hard-14 need)"

  step "1) 선점 조회: 등록할 수 있는 물품인지 서버에 묻는다"
  savetax lookup --id "$PASS_ID" --id "$GAP_ID"

  step "2) 근거 페이지 원문 보관(probe): 게이트는 보관한 원문에 관측가가 있어야 통과시킨다"
  savetax probe --url "$PASS_URL" --need "$PASS_NEED"
  savetax probe --url "$GAP_URL" --need "$GAP_NEED"

  step "3) 참조 본문 $PASS_ID dry-run: 게이트(fact 값 일치·원문 확인·가격 차이) → preview, 서버에 쓰지 않는다"
  savetax submit --claim "$SYNTH/claims/$PASS_ID.json"

  step "4) 사람 승인 → 제출 → 영수증 → 가짜 lint"
  if approve; then
    savetax submit --claim "$SYNTH/claims/$PASS_ID.json" --confirm --poll 15s
  else
    echo "  승인하지 않아 제출하지 않았다"
  fi

  step "5) 게이트가 막는 예 $GAP_ID: 동급이지만 1.196배라 가격 차이 기준(1.2배) 미달"
  if savetax submit --claim "$SYNTH/claims/$GAP_ID.json"; then
    echo "막혀야 하는데 통과했다"
    exit 1
  fi
  echo "→ 예상대로 게이트가 막았다(서버에 보내지 않았다)"

  step "끝"
  echo "합성 데이터 $SYNTH · 1부 기록(bronze·영수증) $DATA · 서버 로그 $LOG"
  ;;
*)
  echo "사용법: studio/demo.sh [run|serve]" >&2
  exit 2
  ;;
esac
