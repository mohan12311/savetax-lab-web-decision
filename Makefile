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
# demo*·studio-* 목표(2부)만 부를 때는 키체인에서 참가 토큰을 읽지 않는다.
ifeq ($(filter-out demo% studio-%,$(MAKECMDGOALS))$(if $(MAKECMDGOALS),,x),)
else ifeq ($(strip $(SAVETAX_TOKEN)),)
SAVETAX_TOKEN := $(shell security find-generic-password -s savetax-lab -a SAVETAX_TOKEN -w 2>/dev/null)
endif
# 명령은 collector/·pipeline/ 안에서 돌므로 데이터 경로를 저장소 루트 기준 절대 경로로 고정한다.
SAVETAX_DATA_DIR := $(abspath $(or $(strip $(SAVETAX_DATA_DIR)),./data))
export

GO := cd collector && go run ./cmd/savetax
# Python 단계는 bronze 파일만 읽고 API를 부르지 않으므로 토큰을 프로세스에 넘기지 않는다.
PY := cd pipeline && env -u SAVETAX_TOKEN uv run

.PHONY: studio-test studio-baseline studio-run studio-grade studio-web studio-review studio-docker studio-docker-down
.PHONY: build check dataset silver candidates lookup facts found listing scrape probe preview submit revise preview-revise submit-status test demo demo-serve demo-test

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
# 발견 상품 카드(남의 등록분 포함)를 끝까지 읽는다. 출처 분포·방법 파악용 — 남의 근거는 우리 근거로 쓰지 않는다(docs/HACKATHON_CLAUDE.md 규칙 2)
found:       ; $(GO) found $(ARGS)
# 등재 상세(데이터 API, 예산 사용). 예: make listing ARGS='--id 002370143_1060000325' 또는 ARGS='--from 파일'
listing:     ; $(GO) listing $(ARGS)
# scrape는 시중 사이트만 부르므로 토큰을 프로세스에 넘기지 않는다. 예: make scrape ARGS='--query S24C310 --top 1'
# SOURCE=naver(네이버 쇼핑 검색 API)는 키가 필요하다. 환경변수가 비면 레시피 안에서 키체인(service=savetax-lab,
# account=NAVER_CLIENT_ID·NAVER_CLIENT_SECRET)에서 읽어 이 프로세스에만 넘긴다(명령줄·파일에 값을 두지 않는다).
NAVER_ENV = export NAVER_CLIENT_ID="$${NAVER_CLIENT_ID:-$$(security find-generic-password -s savetax-lab -a NAVER_CLIENT_ID -w 2>/dev/null)}" \
	NAVER_CLIENT_SECRET="$${NAVER_CLIENT_SECRET:-$$(security find-generic-password -s savetax-lab -a NAVER_CLIENT_SECRET -w 2>/dev/null)}";
unexport NAVER_ENV
scrape:      ; $(if $(filter naver,$(SOURCE)),$(NAVER_ENV)) cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax scrape --source $(or $(SOURCE),danawa) $(ARGS)
# probe도 시중 사이트만 부른다(토큰 제거). 예: make probe ARGS='--url https://… --need 모델명' 또는 ARGS='--from 파일'
probe:       ; cd collector && env -u SAVETAX_TOKEN go run ./cmd/savetax probe $(ARGS)
# 게이트 검사 → preview(dry-run, 선점·쓰기 없음)
preview:     ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json
# 사람의 명시적 승인 뒤에만. 접수되면 제출 횟수를 쓴다(철회해도 돌아오지 않는다)
submit:      ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json --confirm
# 내 기존 제출에 수정을 올린다(물품 그대로, 제출 횟수 안 씀, 수정 뒤 lint 재검사). 사람의 명시적 승인 뒤에만. dry-run은 make preview-revise ID=…
revise:      ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json --revise --confirm
preview-revise: ; $(GO) submit --claim $(SAVETAX_DATA_DIR)/gold/claims/$(ID).json --revise
# 응답을 못 받았거나 lint를 다시 볼 때. 예: make submit-status KEY=<Idempotency-Key>
submit-status: ; $(GO) submit --status $(KEY)
test:        ; cd collector && go test ./... && if [ -f ../pipeline/pyproject.toml ]; then cd ../pipeline && env -u SAVETAX_TOKEN uv run pytest; fi

# --- 2부 데모(studio/): 합성 데이터 + 가짜 대회 서버·판매처. 실제 토큰·키체인·실서버를 쓰지 않는다 ---
demo:        ; env -u SAVETAX_TOKEN studio/demo.sh run
demo-serve:  ; env -u SAVETAX_TOKEN studio/demo.sh serve
demo-test:   ; cd studio/fakeserver && go test ./... && cd ../synth && env -u SAVETAX_TOKEN uv run pytest -q && cd ../../collector && go test ./internal/config/

# --- 2부 워크플로우(studio/): 에이전트 → 답안지 → 웹 검토 → 사람 판정 → 채점. 참가 토큰을 쓰지 않는다 ---
# 에이전트와 도구 서버는 정답 파일(cases.jsonl)을 읽지 않는다. 채점(studio-grade)만 읽는다.
# VARIANT=v2: 표기를 다양하게 그린 합성 데이터(2부 평가용). 비우면 v1(1부 데모와 같은 데이터)
STUDIO_SYNTH := $(abspath studio/demo/$(if $(filter v2,$(VARIANT)),synth-v2,synth))
STUDIO_RUNS := $(abspath studio/demo/runs)
STUDIO_PY = env -u SAVETAX_TOKEN uv run --quiet
# LLM API 키(이 프로젝트 전용 이름): 환경변수가 비면 키체인(service=savetax-lab, account=SAVETAX_STUDIO_OPENAI_KEY /
# SAVETAX_STUDIO_ANTHROPIC_KEY)에서 이 프로세스에만 넘긴다. 전역 OPENAI_API_KEY·ANTHROPIC_API_KEY는 지워 섞이지 않게 한다.
# 값을 명령줄·파일에 두지 않는다(CLAUDE.md 규칙 2).
LLM_ENV = unset OPENAI_API_KEY ANTHROPIC_API_KEY; \
	export SAVETAX_STUDIO_OPENAI_KEY="$${SAVETAX_STUDIO_OPENAI_KEY:-$$(security find-generic-password -s savetax-lab -a SAVETAX_STUDIO_OPENAI_KEY -w 2>/dev/null)}"; \
	export SAVETAX_STUDIO_ANTHROPIC_KEY="$${SAVETAX_STUDIO_ANTHROPIC_KEY:-$$(security find-generic-password -s savetax-lab -a SAVETAX_STUDIO_ANTHROPIC_KEY -w 2>/dev/null)}";
unexport LLM_ENV
LATEST_RUN = $(or $(RUN),$(shell ls -d $(STUDIO_RUNS)/*/ 2>/dev/null | sort | tail -1 | xargs -n1 basename 2>/dev/null))

$(STUDIO_SYNTH)/tasks.jsonl:
	cd studio/synth && $(STUDIO_PY) python -m studio_synth --seed 7 --out $(STUDIO_SYNTH) --shop-base https://127.0.0.1:18444 --variant $(or $(VARIANT),v1)

studio-test:
	cd collector && go test ./cmd/studiocalc
	cd studio/schema && $(STUDIO_PY) pytest -q
	cd studio/synth && $(STUDIO_PY) pytest -q
	cd studio/sheet && $(STUDIO_PY) pytest -q
	cd studio/agent && $(STUDIO_PY) pytest -q
	cd studio/grader && $(STUDIO_PY) pytest -q
	cd studio/review && $(STUDIO_PY) pytest -q
	cd studio/web && npm run --silent typecheck

# LLM 없는 규칙 기준선으로 51건 전체를 돌리고 채점한다(비용 0)
studio-baseline: $(STUDIO_SYNTH)/tasks.jsonl
	cd studio/agent && $(STUDIO_PY) python -m studio_agent run --data $(STUDIO_SYNTH) --out $(STUDIO_RUNS) --model rule-baseline --concurrency 8 >/dev/null
	$(MAKE) --no-print-directory studio-grade

# 실제 LLM API로 돌린다(비용이 든다). 기본은 OpenAI gpt-6.1-terra. 예: make studio-run CASES=hard-01,hard-04 MODEL=gpt-6.1-sol
studio-run: $(STUDIO_SYNTH)/tasks.jsonl
	$(LLM_ENV) cd studio/agent && env -u SAVETAX_TOKEN uv run --quiet python -m studio_agent run --data $(STUDIO_SYNTH) --out $(STUDIO_RUNS) \
		$(if $(MODEL),--model $(MODEL)) --effort $(or $(EFFORT),medium) --repeats $(or $(REPEATS),3) $(if $(CASES),--cases $(CASES)) >/dev/null
	$(MAKE) --no-print-directory studio-grade

# 채점(정답 파일을 읽는 유일한 단계). RUN=<실행 ID>, 비우면 가장 최근 실행
studio-grade:
	cd studio/grader && $(STUDIO_PY) python -m studio_grader $(STUDIO_RUNS)/$(LATEST_RUN) --truth $(STUDIO_SYNTH)/cases.jsonl

studio-web:
	cd studio/web && ([ -d node_modules ] || npm ci --no-audit --no-fund) && npm run --silent build

# 웹 검토 화면: http://127.0.0.1:18480 (루프백만)
studio-review: studio-web
	cd studio/review && $(STUDIO_PY) python -m studio_review --runs $(STUDIO_RUNS) --data $(STUDIO_SYNTH)

# 검토 화면을 Docker로: http://127.0.0.1:18480 (호스트 루프백만). studio/demo를 마운트한다
studio-docker:
	docker compose -f studio/compose.yaml up -d --build
	@echo "검토 화면: http://127.0.0.1:18480"
studio-docker-down:
	docker compose -f studio/compose.yaml down
