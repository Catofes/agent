const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "teacher.html"), "utf8");

test("teacher can control classroom Memory mode and available tools", () => {
  for (const id of [
    "policyMemoryMode",
    "policySkillsEnabled",
    "policyPresets",
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
  assert.match(html, /skills_enabled/);
  assert.match(html, /preset_skills/);
  assert.match(html, /available_preset_skills/);
  assert.match(html, /basic: "基础"/);
  assert.match(html, /physics: "物理"/);
  assert.match(html, /math: "数学"/);
});

test("new classroom inherits the policy currently shown in the controls", () => {
  assert.match(html, /JSON\.stringify\(\{ name, \.\.\.selectedPolicy\(\) \}\)/);
});
