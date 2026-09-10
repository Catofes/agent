const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "teacher.html"), "utf8");

test("teacher can control classroom providers, Memory mode, and available tools", () => {
  for (const id of [
    "policyModelProvider",
    "policySearchProvider",
    "policyDeepSeekSearchChannel",
    "deepSeekSearchChannelField",
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
  assert.match(html, /available_model_providers/);
  assert.match(html, /available_search_providers/);
  assert.match(html, /available_deepseek_search_channels/);
  assert.match(html, /model_provider/);
  assert.match(html, /search_provider/);
  assert.match(html, /deepseek_search_channel/);
  assert.match(html, /"Anthropic Messages"/);
  assert.match(html, /"Responses"/);
  assert.match(html, /服务器没有生成新的设置版本/);
  assert.match(html, /重新保存当前设置/);
  assert.match(html, /option\.value === "deepseek"/);
  assert.match(html, /basic: "基础"/);
  assert.match(html, /physics: "物理"/);
  assert.match(html, /math: "数学"/);
  assert.match(html, /class="policy-select-grid"/);
  assert.match(html, /class="policy-section" aria-labelledby="skillSettingsTitle"/);
  assert.match(html, /class="policy-section" aria-labelledby="toolSettingsTitle"/);
  assert.match(html, /classList\.toggle\("hidden", !deepSeekSearchSelected\)/);
  assert.doesNotMatch(html, /统一控制本场课堂/);
  assert.doesNotMatch(html, /只显示服务器已配置密钥的服务/);
  assert.doesNotMatch(html, /允许使用的工具/);
});

test("new classroom inherits the policy currently shown in the controls", () => {
  assert.match(html, /JSON\.stringify\(\{ name, \.\.\.selectedPolicy\(\) \}\)/);
});

test("teacher sees and can reset per-student token usage", () => {
  for (const id of ["usagePanel", "usageTotal", "usageBreakdown", "usageMeter", "resetUsage"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`));
  }
  assert.match(html, /student_token_budget/);
  assert.match(html, /tokens_in/);
  assert.match(html, /tokens_out/);
  assert.match(html, /\/api\/teacher\/student\/.*\/usage\/reset/);
  assert.match(html, /重置不会删除对话、设计或 Memory/);
});

test("teacher controls an authenticated screen demo while the screen stays read-only", () => {
  for (const id of ["startDemo", "stopDemo", "demoControl", "demoMessages", "demoForm", "demoInput", "sendDemo"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`));
  }
  assert.match(html, /\/api\/teacher\/screen-demo/);
  assert.match(html, /\/api\/teacher\/screen-demo\/chat/);
  assert.match(html, /NDJSONStream\.createParser/);
  assert.doesNotMatch(html, /\/api\/screen\/chat/);
});
