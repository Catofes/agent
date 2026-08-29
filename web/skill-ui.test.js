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
  assert.match(html, /value="explicit"/);
  assert.match(html, /`@\$\{name\} `/);
  assert.match(html, /api\("\/api\/skills"/);
  assert.match(html, /ev\.type === "skill_loaded"/);
  assert.doesNotMatch(html, /Skill：本轮未提供/);
  assert.doesNotMatch(html, /Memory：本轮未读取/);
});
