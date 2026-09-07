const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');

test('student UI manages structured skills and reports only actual context', () => {
  assert.match(html, /id="skillList"/);
  assert.match(html, /id="skillSummary"/);
  assert.match(html, /id="skillWhenToUse"/);
  assert.match(html, /id="skillTriggerMode"/);
  assert.match(html, /id="skillTab"/);
  assert.match(html, /id="toolsPanel"/);
  assert.match(html, /value="explicit"/);
  assert.match(html, /`@\$\{name\} `/);
  assert.match(html, /api\("\/api\/skills"/);
  assert.match(html, /ev\.type === "skill_loaded"/);
  assert.match(html, /state = text\("small", item\.enabled \? mode : "已停用"\)/);
  assert.doesNotMatch(html, /state = text\("small", item\.enabled \? `\$\{mode\} ·/);
  assert.doesNotMatch(html, /Skill：本轮未提供/);
  assert.doesNotMatch(html, /Memory：本轮未读取/);
  assert.match(html, /模板只作为新 Skill 的起点，不会覆盖当前 Skill/);
  assert.match(html, /\$\("templates"\)\.onchange/);
  assert.match(html, /document\.createElement\("optgroup"\)/);
  assert.match(html, /capabilities\?\.preset_skills/);
  assert.doesNotMatch(html, /id="useTemplate"/);
  assert.doesNotMatch(html, /id="calculator"/);
});

test('stream completion renders markdown from the original answer source', () => {
  assert.match(html, /answerSource \+= ev\.delta/);
  assert.match(html, /MarkdownRenderer\.render\(answer, answerSource\)/);
  assert.doesNotMatch(html, /MarkdownRenderer\.render\(answer, answer\.textContent\)/);
});

test('conversation history restores persisted reasoning drafts', () => {
  assert.match(html, /function appendReasoning\(value = "", open = true\)/);
  assert.match(html, /if \(m\.reasoning\) appendReasoning\(m\.reasoning, false\)/);
});

test('student chat input supports multiline text and explicit keyboard sending', () => {
  assert.match(html, /<textarea\s+id="chatInput"/);
  assert.match(html, /Enter 换行，Ctrl\/⌘ \+ Enter 发送/);
  assert.match(html, /event\.ctrlKey \|\| event\.metaKey/);
  assert.match(html, /\$\("chatForm"\)\.requestSubmit\(\)/);
  assert.match(html, /caps\.max_input_chars/);
});
