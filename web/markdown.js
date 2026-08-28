(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.MarkdownRenderer = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  function parseBlocks(source) {
    const lines = String(source || "").replace(/\r\n?/g, "\n").split("\n");
    const blocks = [];
    let index = 0;
    while (index < lines.length) {
      const line = lines[index];
      if (!line.trim()) {
        index++;
        continue;
      }
      const fence = line.match(/^\s*```\s*([\w-]*)\s*$/);
      if (fence) {
        const content = [];
        index++;
        while (index < lines.length && !/^\s*```\s*$/.test(lines[index])) {
          content.push(lines[index++]);
        }
        if (index < lines.length) index++;
        blocks.push({ type: "code", language: fence[1], text: content.join("\n") });
        continue;
      }
      const heading = line.match(/^(#{1,6})\s+(.+)$/);
      if (heading) {
        blocks.push({ type: "heading", level: heading[1].length, text: heading[2] });
        index++;
        continue;
      }
      if (/^\s*>\s?/.test(line)) {
        const content = [];
        while (index < lines.length && /^\s*>\s?/.test(lines[index])) {
          content.push(lines[index++].replace(/^\s*>\s?/, ""));
        }
        blocks.push({ type: "quote", text: content.join("\n") });
        continue;
      }
      const unordered = line.match(/^\s*[-+*]\s+(.+)$/);
      const ordered = line.match(/^\s*\d+[.)]\s+(.+)$/);
      if (unordered || ordered) {
        const type = ordered ? "ordered-list" : "unordered-list";
        const pattern = ordered ? /^\s*\d+[.)]\s+(.+)$/ : /^\s*[-+*]\s+(.+)$/;
        const items = [];
        while (index < lines.length) {
          const item = lines[index].match(pattern);
          if (!item) break;
          items.push(item[1]);
          index++;
        }
        blocks.push({ type, items });
        continue;
      }
      if (/^\s*(?:---+|___+|\*\*\*+)\s*$/.test(line)) {
        blocks.push({ type: "rule" });
        index++;
        continue;
      }
      const content = [line];
      index++;
      while (
        index < lines.length &&
        lines[index].trim() &&
        !/^\s*```/.test(lines[index]) &&
        !/^(#{1,6})\s+/.test(lines[index]) &&
        !/^\s*>\s?/.test(lines[index]) &&
        !/^\s*(?:[-+*]|\d+[.)])\s+/.test(lines[index])
      ) {
        content.push(lines[index++]);
      }
      blocks.push({ type: "paragraph", text: content.join("\n") });
    }
    return blocks;
  }

  function safeLink(value) {
    const href = String(value || "").trim();
    return /^(?:https?:|mailto:)/i.test(href) ? href : "";
  }

  function appendInline(parent, source, doc) {
    const text = String(source || "");
    const pattern = /(`[^`\n]+`|\*\*[^*\n]+\*\*|__[^_\n]+__|\*[^*\n]+\*|_([^_\n]+)_|\[[^\]\n]+\]\([^\s)]+\))/g;
    let cursor = 0;
    for (const match of text.matchAll(pattern)) {
      if (match.index > cursor) appendTextWithBreaks(parent, text.slice(cursor, match.index), doc);
      const token = match[0];
      let element;
      if (token.startsWith("`")) {
        element = doc.createElement("code");
        element.textContent = token.slice(1, -1);
      } else if (token.startsWith("**") || token.startsWith("__")) {
        element = doc.createElement("strong");
        element.textContent = token.slice(2, -2);
      } else if (token.startsWith("[")) {
        const parts = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
        const href = safeLink(parts && parts[2]);
        element = href ? doc.createElement("a") : doc.createElement("span");
        element.textContent = parts ? parts[1] : token;
        if (href) {
          element.href = href;
          element.target = "_blank";
          element.rel = "noopener noreferrer";
        }
      } else {
        element = doc.createElement("em");
        element.textContent = token.slice(1, -1);
      }
      parent.append(element);
      cursor = match.index + token.length;
    }
    if (cursor < text.length) appendTextWithBreaks(parent, text.slice(cursor), doc);
  }

  function appendTextWithBreaks(parent, value, doc) {
    String(value).split("\n").forEach((part, index) => {
      if (index) parent.append(doc.createElement("br"));
      if (part) parent.append(doc.createTextNode(part));
    });
  }

  function render(container, source) {
    const doc = container.ownerDocument || document;
    const nodes = parseBlocks(source).map((block) => {
      if (block.type === "code") {
        const pre = doc.createElement("pre"), code = doc.createElement("code");
        if (block.language) code.className = "language-" + block.language;
        code.textContent = block.text;
        pre.append(code);
        return pre;
      }
      if (block.type === "rule") return doc.createElement("hr");
      if (block.type === "heading") {
        const heading = doc.createElement("h" + block.level);
        appendInline(heading, block.text, doc);
        return heading;
      }
      if (block.type === "ordered-list" || block.type === "unordered-list") {
        const list = doc.createElement(block.type === "ordered-list" ? "ol" : "ul");
        block.items.forEach((item) => {
          const li = doc.createElement("li");
          appendInline(li, item, doc);
          list.append(li);
        });
        return list;
      }
      const element = doc.createElement(block.type === "quote" ? "blockquote" : "p");
      appendInline(element, block.text, doc);
      return element;
    });
    container.replaceChildren(...nodes);
    container.classList.add("markdown-body");
  }

  return { parseBlocks, render, safeLink };
});
