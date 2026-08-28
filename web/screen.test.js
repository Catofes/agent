const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');

test('shared UI stylesheet preserves the native hidden state', () => {
  const css = fs.readFileSync(`${__dirname}/ui.css`, 'utf8');
  assert.match(css, /\[hidden\]\s*\{\s*display:\s*none\s*!important;/);
});

function screenRuntime({ animationFrames = true } = {}) {
  const html = fs.readFileSync(`${__dirname}/screen.html`, 'utf8');
  const match = html.match(/<script>([\s\S]*)<\/script>/);
  assert.ok(match, 'screen script missing');

  const elements = new Map();
  const createElement = () => ({
    hidden: false,
    textContent: '',
    className: '',
    children: [],
    classList: {
      toggle() {},
    },
    replaceChildren(...children) {
      this.children = children;
    },
  });
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, createElement());
      return elements.get(id);
    },
    createElement,
  };
  const requests = [];
  let source;
  class EventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      source = this;
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
  }
  const context = {
    console: { info() {}, warn() {}, error() {} },
    document,
    EventSource,
    fetch: async (url, options) => {
      requests.push({ url, options });
      return { ok: true, status: 200 };
    },
    requestAnimationFrame: callback => {
      if (animationFrames) callback();
    },
  };
  vm.runInNewContext(match[1], context, { filename: 'screen.html' });
  return { elements, requests, source };
}

test('screen SSE event renders DOM and acknowledges the rendered spotlight', async () => {
  const runtime = screenRuntime();
  const state = {
    spotlight_id: 'screen_test',
    revision: 2,
    name: '张三',
    persona: '耐心的数学老师',
    skill_md: '先分析，再计算',
    tools: ['calculator'],
    max_turns: 3,
    messages: [
      { role: 'user', content: '23*17 是多少？', conversation: '数学讨论' },
      { role: 'assistant', content: '391', conversation: '数学讨论', current: true },
    ],
    empty: false,
  };

  runtime.source.listeners.get('screen')({ data: JSON.stringify(state) });
  await Promise.resolve();

  assert.equal(runtime.elements.get('empty').hidden, true);
  assert.equal(runtime.elements.get('screen').hidden, false);
  assert.equal(runtime.elements.get('name').textContent, '张三 的 Agent');
  assert.equal(runtime.elements.get('persona').textContent, '耐心的数学老师');
  assert.equal(runtime.elements.get('messages').children.length, 3);
  assert.equal(runtime.elements.get('messages').children[0].textContent, '数学讨论');
  assert.equal(runtime.elements.get('messages').children[2].textContent, 'Agent：391');
  assert.match(runtime.elements.get('messages').children[2].className, /current/);
  assert.equal(runtime.requests.length, 1);
  assert.equal(runtime.requests[0].url, '/api/screen/ack');
  assert.deepEqual(JSON.parse(runtime.requests[0].options.body), { spotlight_id: 'screen_test' });

  runtime.source.listeners.get('screen')({ data: JSON.stringify({ ...state, spotlight_id: 'screen_old', revision: 1, name: '旧画面' }) });
  assert.equal(runtime.elements.get('name').textContent, '张三 的 Agent');
  assert.equal(runtime.requests.length, 1);
});

test('screen acknowledgement does not depend on animation frames', async () => {
  const runtime = screenRuntime({ animationFrames: false });
  runtime.source.listeners.get('screen')({ data: JSON.stringify({
    spotlight_id: 'screen_background',
    revision: 3,
    name: '后台大屏',
    messages: [],
    empty: false,
  }) });
  await Promise.resolve();

  assert.equal(runtime.requests.length, 1);
  assert.equal(runtime.requests[0].url, '/api/screen/ack');
});

test('screen render errors are visible and are not acknowledged', () => {
  const runtime = screenRuntime();
  runtime.source.listeners.get('screen')({ data: '{invalid json' });

  assert.match(runtime.elements.get('connection').textContent, /渲染失败/);
  assert.equal(runtime.requests.length, 0);
});
