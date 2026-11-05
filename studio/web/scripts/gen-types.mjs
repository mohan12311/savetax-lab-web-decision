// 답안지·사람 판정의 TypeScript 타입을 JSON Schema 원본(studio/schema)에서 만든다. 손으로 고치지 않는다.
import { compileFromFile } from "json-schema-to-typescript";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const schemaDir = path.resolve(here, "../../schema");
const out = path.resolve(here, "../src/types");
for (const [name, file] of [["AnswerSheet", "answer-sheet"], ["Judgment", "judgment"]]) {
  const ts = await compileFromFile(path.join(schemaDir, `${file}.schema.json`), {
    bannerComment: `/* 자동 생성: studio/schema/${file}.schema.json → npm run gen:types. 손으로 고치지 않는다. */`,
    cwd: schemaDir,
    additionalProperties: false,
    declareExternallyReferenced: true,
  });
  writeFileSync(path.join(out, `${file}.ts`), ts);
  console.log(`${name} → src/types/${file}.ts`);
}
