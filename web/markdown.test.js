const test = require("node:test");
const assert = require("node:assert/strict");
const { parseBlocks, render, safeLink } = require("./markdown.js");

function fakeDocument() {
  function element(tagName) {
    return {
      tagName: tagName.toUpperCase(),
      children: [],
      className: "",
      textContent: "",
      append(...nodes) { this.children.push(...nodes); },
    };
  }
  return {
    createElement: element,
    createTextNode(textContent) { return { tagName: "#TEXT", textContent }; },
  };
}

function fakeContainer() {
  const ownerDocument = fakeDocument();
  return {
    ownerDocument,
    children: [],
    classes: [],
    replaceChildren(...nodes) { this.children = nodes; },
    classList: { add() {} },
  };
}

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

test("chat markdown preserves ordinary and explicit hard line breaks", () => {
  const container = fakeContainer();
  render(container, "第一行\n第二行  \n第三行\\\n第四行");

  assert.equal(container.children.length, 1);
  assert.equal(container.children[0].tagName, "P");
  assert.deepEqual(
    container.children[0].children.map((node) => [node.tagName, node.textContent || ""]),
    [
      ["#TEXT", "第一行"], ["BR", ""],
      ["#TEXT", "第二行"], ["BR", ""],
      ["#TEXT", "第三行"], ["BR", ""],
      ["#TEXT", "第四行"],
    ],
  );
});

test("list continuation lines and blank-separated items remain in one list", () => {
  const blocks = parseBlocks("- 第一点\n  续行说明\n\n- 第二点\n仍然属于第二点");

  assert.deepEqual(blocks, [{
    type: "unordered-list",
    items: ["第一点\n续行说明", "第二点\n仍然属于第二点"],
  }]);
});

test("blank lines create paragraphs while Windows line endings stay normalized", () => {
  const blocks = parseBlocks("第一行\r\n第二行\r\n\r\n下一段");

  assert.deepEqual(blocks, [
    { type: "paragraph", text: "第一行\n第二行" },
    { type: "paragraph", text: "下一段" },
  ]);
});

test("markdown parser recognizes dollar and bracket display formulas", () => {
  const blocks = parseBlocks("推导如下：\n\n$$\n\\int_0^1 x^2 \\, dx = \\frac{1}{3}\n$$\n\n\\[E=mc^2\\]");

  assert.deepEqual(blocks, [
    { type: "paragraph", text: "推导如下：" },
    { type: "math", text: "\\int_0^1 x^2 \\, dx = \\frac{1}{3}" },
    { type: "math", text: "E=mc^2" },
  ]);
});

test("markdown renders inline formulas while leaving code and escaped dollars alone", () => {
  const calls = [];
  globalThis.katex = {
    render(source, element, options) {
      calls.push([source, options.displayMode, options.trust]);
      element.textContent = `math:${source}`;
    },
  };
  const container = fakeContainer();
  render(container, "勾股定理 $a^2+b^2=c^2$，代码 `$not_math$`，价格 \\$5。\\(x+1\\)");
  delete globalThis.katex;

  assert.deepEqual(calls, [
    ["a^2+b^2=c^2", false, false],
    ["x+1", false, false],
  ]);
  const paragraph = container.children[0];
  assert.equal(paragraph.children.filter((node) => node.className === "math-inline").length, 2);
  assert.equal(paragraph.children.some((node) => node.tagName === "CODE" && node.textContent === "$not_math$"), true);
});

test("markdown renders display formulas through KaTeX with safe options", () => {
  let options;
  globalThis.katex = { render(_source, _element, value) { options = value; } };
  const container = fakeContainer();
  render(container, "$$\\frac{a}{b}$$");
  delete globalThis.katex;

  assert.equal(container.children[0].className, "math-block");
  assert.deepEqual(
    { displayMode: options.displayMode, throwOnError: options.throwOnError, trust: options.trust },
    { displayMode: true, throwOnError: false, trust: false },
  );
});
