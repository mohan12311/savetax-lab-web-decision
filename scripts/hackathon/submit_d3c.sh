#!/bin/bash
# 10/23 사용자 승인: 마지막 6칸 플룸 6건(본문 data/gold/claims_new)을 앞 6건 제출이 끝난 뒤 한 번씩 제출한다.
# 제출 직전 preview의 서버 예상액이 승인 때 보여 준 금액과 같을 때만 낸다. make submit을 한 번이라도 부른 물품은 다시 내지 않는다.
cd "$(dirname "$0")/../.." || exit 1  # 저장소 루트(공개 전에 홈 경로를 지웠다)
until grep -q '^끝' /private/tmp/claude-501/-Users-sa-dev-savetax-lab/add911ee-8dc3-4be5-9e38-5b54d5e2e07d/scratchpad/submit_d3b.out 2>/dev/null; do sleep 30; done
LIST=""  # 공개본에서 지움: 사용자가 승인한 "물품번호:승인 때 본 서버 예상액" 6쌍
for pair in $LIST; do
  id=${pair%%:*}; amt=${pair#*:}
  pv=$(make preview ID=../claims_new/$id 2>&1)
  line=$(echo "$pv" | grep -m1 "^preview:")
  if ! echo "$line" | grep -q "ok=true · 서버 예상액 ${amt}원"; then
    echo "$(date +%m-%d\ %H:%M) SKIP $id (제출 안 함) → ${line:-$(echo "$pv" | grep -m1 -E '오류|등록할 수 없는|막혔다|거절|한도')}"; continue
  fi
  echo "$(date +%m-%d\ %H:%M) == $id 제출"
  make submit ID=../claims_new/$id 2>&1 | grep -v '^cd ' | grep -E '^제출|^접수|오류|실패|등록할 수 없는|막혔다|거절|한도' | head -6
done
echo "끝"
