const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");

test("student login asks only for the student id", () => {
  assert.match(html, /id="studentId"/);
  assert.doesNotMatch(html, /id="initial"|name_initial|姓名首字/);
  assert.match(html, /id: \$\("studentId"\)\.value/);
});
