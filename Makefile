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
PY := cd pipeline && uv run

.PHONY: build check dataset silver candidates lookup facts watch scrape agent preview submit report test

build:       ; cd collector && go build -o bin/savetax ./cmd/savetax
check:       ; $(GO) check
dataset:     ; $(GO) dataset
silver:      ; $(PY) python -m savetax_pipeline.silver
candidates:  ; $(PY) python -m savetax_pipeline.candidates
lookup:      ; $(GO) lookup --from ../data/gold/candidates.parquet
facts:       ; $(GO) facts --top 500
watch:       ; $(GO) watch
# scrape는 시중 사이트만 부르므로 토큰을 프로세스에 넘기지 않는다. 예: make scrape ARGS='--query S24C310 --top 1'
scrape:      ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax scrape --source $(or $(SOURCE),danawa) $(ARGS)
agent:       ; cd agent && uv run python -m agent.run
preview:     ; $(GO) submit --claim ../data/gold/claims/$(ID).json
submit:      ; $(GO) submit --claim ../data/gold/claims/$(ID).json --confirm
report:      ; $(PY) python -m savetax_pipeline.report
test:        ; cd collector && go test ./... && if [ -f ../pipeline/pyproject.toml ]; then cd ../pipeline && uv run pytest; fi
