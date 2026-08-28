const test = require("node:test");
const assert = require("node:assert/strict");
const { parseBlocks, safeLink } = require("./markdown.js");

test("markdown parser recognizes classroom response structures", () => {
  const blocks = parseBlocks("# 结论\n\n- 第一点\n- 第二点\n\n```js\nalert(1)\n```");
  assert.deepEqual(blocks.map((block) => block.type), [
    "heading",
    "unordered-list",
    "code",
  ]);
  assert.equal(blocks[2].language, "js");
  assert.equal(blocks[2].text, "alert(1)");
});

test("markdown links allow only explicit safe protocols", () => {
  assert.equal(safeLink("https://example.com"), "https://example.com");
  assert.equal(safeLink("mailto:teacher@example.com"), "mailto:teacher@example.com");
  assert.equal(safeLink("javascript:alert(1)"), "");
  assert.equal(safeLink("data:text/html,bad"), "");
});
