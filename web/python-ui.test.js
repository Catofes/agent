const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const teacher = fs.readFileSync(path.join(__dirname, "teacher.html"), "utf8");

test("student Python lab supports manual execution, files, cancellation and explanation", () => {
  for (const id of [
    "pythonOption",
    "pythonLab",
    "pythonCode",
    "pythonStdin",
    "pythonFiles",
    "runPython",
    "stopPython",
    "explainPython",
    "artifactList",
  ]) {
    assert.match(html, new RegExp(`id=["']${id}["']`));
  }
  assert.match(html, /fetch\("\/api\/python\/run"/);
  assert.match(html, /fetch\("\/api\/artifacts"/);
  assert.match(html, /signal: pythonController\.signal/);
  assert.match(html, /ev\.artifacts\?\.length/);
  assert.match(html, /文件已清理/);
  assert.match(html, /直接运行不会消耗一次 Agent 对话/);
});

test("Python is exposed through student and teacher tool controls only when available", () => {
  assert.match(html, /allowed_tools\.includes\("python_execute"\)/);
  assert.match(html, /function renderCapabilities\(changed = false\)/);
  assert.match(html, /pythonLab"\)\.classList\.toggle\("hidden", !pythonAllowed\)/);
  assert.match(html, /无需重新登录/);
  assert.match(html, /python_execute: "Python 执行"/);
  assert.match(teacher, /python_execute: "Python 执行"/);
});

test("Python history uses a bounded readable action summary", () => {
  assert.match(html, /function historyToolSummary\(call\)/);
  assert.match(html, /运行 \$\{chars\} 字 Python 代码/);
  assert.match(html, /summary: historyToolSummary\(c\)/);
});
