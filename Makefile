SHELL := /bin/bash
# .env의 빈 `SAVETAX_TOKEN=`이 셸에서 export한 토큰을 덮지 않게 먼저 기억해 둔다.
_ENV_TOKEN := $(SAVETAX_TOKEN)
unexport _ENV_TOKEN
-include .env
ifeq ($(strip $(SAVETAX_TOKEN)),)
SAVETAX_TOKEN := $(_ENV_TOKEN)
endif
# 그래도 비어 있으면 macOS 키체인(service=savetax-lab, account=SAVETAX_TOKEN)에서 읽는다.
# 토큰을 저장소 파일·명령줄에 두지 않기 위함. 키체인이 없거나 항목이 없으면 빈 값 그대로.
ifeq ($(strip $(SAVETAX_TOKEN)),)
SAVETAX_TOKEN := $(shell security find-generic-password -s savetax-lab -a SAVETAX_TOKEN -w 2>/dev/null)
endif
# 명령은 collector/·pipeline/ 안에서 돌므로 데이터 경로를 저장소 루트 기준 절대 경로로 고정한다.
SAVETAX_DATA_DIR := $(abspath $(or $(strip $(SAVETAX_DATA_DIR)),./data))
export

GO := cd collector && go run ./cmd/savetax
# Python 단계는 bronze 파일만 읽고 API를 부르지 않으므로 토큰을 프로세스에 넘기지 않는다.
PY := cd pipeline && env -u SAVETAX_TOKEN uv run

.PHONY: build check dataset silver candidates lookup facts watch scrape probe agent preview submit submit-status report test

build:       ; cd collector && go build -o bin/savetax ./cmd/savetax
check:       ; $(GO) check
dataset:     ; $(GO) dataset
silver:      ; $(PY) python -m savetax_pipeline.silver
# RETAIL=48,56처럼 시중가 범위 분류(앞 2자리)를 바꾼다. 비우면 코드 기본값
candidates:  ; $(PY) python -m savetax_pipeline.candidates $(if $(RETAIL),--retail-segments $(RETAIL))
# 후보 선점 조회(대회 API, 횟수 안 씀). 끝나면 make silver && make candidates로 상태를 합친다.
# IDS=retail_ids면 시중가 범위(data/gold/retail_ids.txt)만 묻는다.
lookup:      ; $(GO) lookup --from $(SAVETAX_DATA_DIR)/gold/$(or $(IDS),lookup_ids).txt $(ARGS)
# 지정한 물품만 조회한다(첫 호출이 서버 스냅샷을 만들어 남에게도 보이는 신호가 된다 — 제출 직전 후보에만). 예: make facts ID=24883583 ARGS='--draft'
facts:       ; $(GO) facts --id $(ID) $(ARGS)
watch:       ; $(GO) watch
# scrape는 시중 사이트만 부르므로 토큰을 프로세스에 넘기지 않는다. 예: make scrape ARGS='--query S24C310 --top 1'
scrape:      ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax scrape --source $(or $(SOURCE),danawa) $(ARGS)
# probe도 시중 사이트만 부른다(토큰 제거). 예: make probe ARGS='--url https://… --need 모델명' 또는 ARGS='--from 파일'
probe:       ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax probe $(ARGS)
agent:       ; cd agent && uv run python -m agent.run
# 게이트 검사 → preview(dry-run, 선점·쓰기 없음)
preview:     ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json
# 사람의 명시적 승인 뒤에만. 접수되면 제출 횟수를 쓴다(철회해도 돌아오지 않는다)
submit:      ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json --confirm
# 응답을 못 받았거나 lint를 다시 볼 때. 예: make submit-status KEY=<Idempotency-Key>
submit-status: ; $(GO) submit --status $(KEY)
report:      ; $(PY) python -m savetax_pipeline.report
test:        ; cd collector && go test ./... && if [ -f ../pipeline/pyproject.toml ]; then cd ../pipeline && env -u SAVETAX_TOKEN uv run pytest; fi
