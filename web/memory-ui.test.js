const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");

test("student workbench exposes the complete Memory confirmation workflow", () => {
  for (const id of [
    "memoryEnabled",
    "refreshMemory",
    "clearMemory",
    "memoryUsage",
    "memoryList",
  ]) {
    assert.match(html, new RegExp(`id=["']${id}["']`));
  }
  assert.match(html, /只有你确认的条目/);
  assert.match(html, /新建课堂场次不会带入/);
  assert.match(html, /contextReceipt\(ev\)/);
  assert.match(html, /id=["']memoryNotice["']/);
  assert.match(html, /addEventListener\(["']memory["']/);
  assert.match(html, /memory_status/);
  assert.match(html, /memory_recalled/);
  assert.match(html, /recall_memory:\s*["']读取 Memory["']/);
  assert.match(html, /撤销这条/);
  assert.match(html, /encodeURIComponent\(first\.id\)/);
  assert.doesNotMatch(html, /setTimeout\(\(\) => loadMemory/);
});

test("Memory and context receipts use text-only DOM rendering", () => {
  assert.doesNotMatch(html, /\.innerHTML\s*=/);
  assert.match(html, /content\.textContent/);
  assert.match(html, /text\("div", item\.content\)/);
});

test("conversations are exposed as browser-style tabs", () => {
  assert.match(html, /id=["']conversationTabs["']/);
  assert.match(html, /role=["']tablist["']/);
  assert.match(html, /className\s*=\s*[\s\S]*conversation-tab/);
  assert.match(html, /aria-selected/);
  assert.match(html, /className = "conversation-close"/);
  assert.match(html, /method: "DELETE"/);
  assert.match(html, /消息会被永久删除/);
  assert.doesNotMatch(html, /id=["']conversations["']/);
});

test("Soul, Skill, Memory and Tools share the design area as accessible tabs", () => {
  for (const panel of ["soulPanel", "skillPanel", "memoryPanel", "toolsPanel"]) {
    assert.match(html, new RegExp(`aria-controls=["']${panel}["']`));
    assert.match(html, new RegExp(`id=["']${panel}["']`));
  }
  assert.match(html, /data-design-tab/);
  assert.match(html, /role=["']tabpanel["']/);
});

test("agent step control defaults to 45 and allows up to 60", () => {
  assert.match(
    html,
    /id=["']maxTurns["'][^>]*min=["']1["'][^>]*max=["']60["'][^>]*value=["']45["']/,
  );
  assert.match(html, /id=["']turnValue["']>45</);
});
