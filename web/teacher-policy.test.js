const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "teacher.html"), "utf8");

test("teacher can control classroom Memory mode and available tools", () => {
  for (const id of [
    "policyMemoryMode",
    "policyTools",
    "savePolicy",
    "policyStatus",
  ]) {
    assert.match(html, new RegExp(`id=["']${id}["']`));
  }
  for (const mode of ["disabled", "review_required", "adaptive"]) {
    assert.match(html, new RegExp(`value=["']${mode}["']`));
  }
  assert.match(html, /\/api\/teacher\/policy/);
  assert.match(html, /allowed_tools/);
});

test("new classroom inherits the policy currently shown in the controls", () => {
  assert.match(html, /JSON\.stringify\(\{ name, \.\.\.selectedPolicy\(\) \}\)/);
});
