(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.MarkdownRenderer = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  function matchListItem(line) {
    let match = String(line || "").match(/^(\s*)[-+*]\s+(.+)$/);
    if (match) return { type: "unordered-list", indent: match[1].length, text: match[2] };
    match = String(line || "").match(/^(\s*)\d+[.)]\s+(.+)$/);
    if (match) return { type: "ordered-list", indent: match[1].length, text: match[2] };
    return null;
  }

  function isRule(line) {
    return /^\s*(?:(?:-\s*){3,}|(?:_\s*){3,}|(?:\*\s*){3,})$/.test(line);
  }

  function startsBlock(line) {
    return (
      /^\s*```/.test(line) ||
      /^(#{1,6})\s+/.test(line) ||
      /^\s*>\s?/.test(line) ||
      Boolean(matchListItem(line)) ||
      isRule(line)
    );
  }

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
      if (index + 1 < lines.length && /^\s*(?:=+|-+)\s*$/.test(lines[index + 1])) {
        blocks.push({
          type: "heading",
          level: lines[index + 1].includes("=") ? 1 : 2,
          text: line.trim(),
        });
        index += 2;
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
      if (isRule(line)) {
        blocks.push({ type: "rule" });
        index++;
        continue;
      }
      const firstItem = matchListItem(line);
      if (firstItem) {
        const type = firstItem.type;
        const items = [];
        let current = "";
        while (index < lines.length) {
          const item = matchListItem(lines[index]);
          if (item && item.type === type) {
            if (current) items.push(current);
            current = item.text;
            index++;
            continue;
          }
          if (!lines[index].trim()) {
            let next = index + 1;
            while (next < lines.length && !lines[next].trim()) next++;
            const nextItem = next < lines.length ? matchListItem(lines[next]) : null;
            if (nextItem && nextItem.type === type) {
              index = next;
              continue;
            }
            break;
          }
          // Models commonly emit a long list item on several source lines. Keep
          // lazy or indented continuation lines attached to the current item.
          if (current && !startsBlock(lines[index])) {
            current += "\n" + lines[index].trimStart();
            index++;
            continue;
          }
          break;
        }
        if (current) items.push(current);
        blocks.push({ type, items });
        continue;
      }
      const content = [line];
      index++;
      while (
        index < lines.length &&
        lines[index].trim() &&
        !startsBlock(lines[index]) &&
        !(index + 1 < lines.length && /^\s*(?:=+|-+)\s*$/.test(lines[index + 1]))
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
      // In chat mode every source newline is visible. Markdown hard-break
      // markers are therefore redundant and should not leak into the message.
      const visible = part.replace(/[ \t]+$/, "").replace(/\\$/, "");
      if (visible) parent.append(doc.createTextNode(visible));
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
