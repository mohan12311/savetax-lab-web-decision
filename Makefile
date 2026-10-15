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

.PHONY: build check dataset silver candidates lookup facts watch scrape probe agent preview submit report test

build:       ; cd collector && go build -o bin/savetax ./cmd/savetax
check:       ; $(GO) check
dataset:     ; $(GO) dataset
silver:      ; $(PY) python -m savetax_pipeline.silver
candidates:  ; $(PY) python -m savetax_pipeline.candidates
# 후보 선점 조회(대회 API, 횟수 안 씀). 끝나면 make silver && make candidates로 상태를 합친다.
# IDS=retail_ids면 시중가 범위(data/gold/retail_ids.txt)만 묻는다.
lookup:      ; $(GO) lookup --from $(SAVETAX_DATA_DIR)/gold/$(or $(IDS),lookup_ids).txt $(ARGS)
facts:       ; $(GO) facts --top 500
watch:       ; $(GO) watch
# scrape는 시중 사이트만 부르므로 토큰을 프로세스에 넘기지 않는다. 예: make scrape ARGS='--query S24C310 --top 1'
scrape:      ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax scrape --source $(or $(SOURCE),danawa) $(ARGS)
# probe도 시중 사이트만 부른다(토큰 제거). 예: make probe ARGS='--url https://… --need 모델명' 또는 ARGS='--from 파일'
probe:       ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax probe $(ARGS)
agent:       ; cd agent && uv run python -m agent.run
preview:     ; $(GO) submit --claim ../data/gold/claims/$(ID).json
submit:      ; $(GO) submit --claim ../data/gold/claims/$(ID).json --confirm
report:      ; $(PY) python -m savetax_pipeline.report
test:        ; cd collector && go test ./... && if [ -f ../pipeline/pyproject.toml ]; then cd ../pipeline && env -u SAVETAX_TOKEN uv run pytest; fi
