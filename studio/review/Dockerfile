# 검토 화면(studio/review + studio/web) 이미지. 빌드 컨텍스트는 저장소 루트다(studio/compose.yaml).
# 데이터(합성 데이터, 실행 결과, 사람 판정)는 이미지에 넣지 않고 /data로 마운트한다. 대회 원문(data/)은 .dockerignore로 뺀다.

# 1) 웹 화면 빌드: 타입은 JSON Schema 원본(studio/schema)에서 만든다
FROM node:22-alpine AS web
WORKDIR /src/studio/web
COPY studio/schema/ /src/studio/schema/
COPY studio/web/package.json studio/web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY studio/web/ ./
RUN npm run build

# 2) 백엔드: 표준 라이브러리 HTTP 서버 + 스키마 검증(jsonschema)만 필요하다
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/studio/schema/src:/app/studio/sheet/src:/app/studio/review/src
RUN pip install --no-cache-dir "jsonschema==4.26.0"
WORKDIR /app
COPY studio/schema/ /app/studio/schema/
COPY studio/sheet/src/ /app/studio/sheet/src/
COPY studio/review/src/ /app/studio/review/src/
COPY --from=web /src/studio/web/dist/ /app/studio/web/dist/
RUN useradd --create-home --uid 1000 app
USER app
EXPOSE 18480
# 컨테이너 안에서는 0.0.0.0으로 받는다. 호스트 쪽은 compose가 127.0.0.1에만 연다
CMD ["python", "-m", "studio_review", "--runs", "/data/runs", "--data", "/data/synth", "--host", "0.0.0.0", "--port", "18480"]
