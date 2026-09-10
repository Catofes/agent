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
    attributes: new Map(),
    classList: {
      toggle() {},
    },
    dataset: {},
    setAttribute(name, value) {
      this.attributes.set(name, value);
    },
    append(...children) {
      this.children.push(...children);
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
    MarkdownRenderer: {
      render(element, value) {
        element.textContent = value;
      },
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
  assert.equal(runtime.elements.get('conversationTabs').children.length, 1);
  assert.equal(runtime.elements.get('conversationTabs').children[0].textContent, '数学讨论');
  assert.equal(runtime.elements.get('messages').children.length, 2);
  assert.equal(runtime.elements.get('messages').children[1].children[0].textContent, 'Agent');
  assert.equal(runtime.elements.get('messages').children[1].children[1].textContent, '391');
  assert.match(runtime.elements.get('messages').children[1].className, /current/);
  assert.equal(runtime.elements.get('messages').scrollTop, 0);
  assert.equal(runtime.requests.length, 1);
  assert.equal(runtime.requests[0].url, '/api/screen/ack');
  assert.deepEqual(JSON.parse(runtime.requests[0].options.body), { spotlight_id: 'screen_test' });

  runtime.source.listeners.get('screen')({ data: JSON.stringify({ ...state, spotlight_id: 'screen_old', revision: 1, name: '旧画面' }) });
  assert.equal(runtime.elements.get('name').textContent, '张三 的 Agent');
  assert.equal(runtime.requests.length, 1);
});

test('screen tabs give configuration and conversation separate bounded panels', () => {
  const runtime = screenRuntime();
  const config = runtime.elements.get('configPanel');
  const conversation = runtime.elements.get('conversationPanel');

  assert.equal(config.hidden, false);
  assert.equal(conversation.hidden, true);
  assert.equal(runtime.elements.get('testPanel').hidden, true);
  assert.equal(runtime.elements.get('configTab').attributes.get('aria-selected'), 'true');
  runtime.elements.get('conversationTab').onclick();
  assert.equal(config.hidden, true);
  assert.equal(conversation.hidden, false);
  assert.equal(runtime.elements.get('conversationTab').attributes.get('aria-selected'), 'true');
});

test('screen conversation sub-tabs split sessions and select the current one', () => {
  const runtime = screenRuntime();
  runtime.source.listeners.get('screen')({ data: JSON.stringify({
    spotlight_id: 'screen_sessions',
    revision: 3,
    name: '张三',
    messages: [
      { role: 'user', content: '第一场问题', conversation: '数学讨论', conversation_key: 'a' },
      { role: 'assistant', content: '第一场回答', conversation: '数学讨论', conversation_key: 'a' },
      { role: 'user', content: '第二场问题', conversation: '数学讨论', conversation_key: 'b', current: true },
      { role: 'assistant', content: '第二场回答', conversation: '数学讨论', conversation_key: 'b', current: true },
    ],
    empty: false,
  }) });

  const tabs = runtime.elements.get('conversationTabs').children;
  assert.equal(tabs.length, 2);
  assert.equal(tabs[0].textContent, '数学讨论（1）');
  assert.equal(tabs[1].textContent, '数学讨论（2）');
  assert.equal(tabs[1].attributes.get('aria-selected'), 'true');
  assert.equal(runtime.elements.get('conversationSummary').textContent, '共 2 场对话');
  assert.equal(runtime.elements.get('messages').children.length, 2);
  assert.equal(runtime.elements.get('messages').children[0].children[1].textContent, '第二场问题');

  tabs[0].onclick();
  assert.equal(tabs[0].attributes.get('aria-selected'), 'true');
  assert.equal(runtime.elements.get('messages').children[0].children[1].textContent, '第一场问题');
});

test('screen demo event switches to the live test tab and renders shared state', () => {
  const runtime = screenRuntime();
  runtime.source.listeners.get('demo')({ data: JSON.stringify({
    id: 'screen_demo_1',
    revision: 2,
    active: true,
    name: '张三',
    status: '正在回答…',
    running: true,
    messages: [
      { role: 'user', content: '现场问题', current: true },
      { role: 'assistant', content: '现场回答', current: true },
    ],
  }) });

  assert.equal(runtime.elements.get('configPanel').hidden, true);
  assert.equal(runtime.elements.get('testPanel').hidden, false);
  assert.equal(runtime.elements.get('demoName').textContent, '张三 的现场测试');
  assert.equal(runtime.elements.get('demoStatus').textContent, '正在回答…');
  assert.equal(runtime.elements.get('demoMessages').children.length, 2);
  assert.equal(runtime.elements.get('demoMessages').children[0].children[0].textContent, '教师提问');
  assert.equal(runtime.elements.get('demoMessages').children[1].children[1].textContent, '现场回答');
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

test('screen collapses intermediate tool activity by default', async () => {
  const runtime = screenRuntime();
  runtime.source.listeners.get('screen')({ data: JSON.stringify({
    spotlight_id: 'screen_process',
    revision: 4,
    name: '张三',
    messages: [
      { role: 'user', content: '帮我计算', conversation: '数学讨论' },
      { role: 'assistant', content: '调用工具：calculator', conversation: '数学讨论', process: true },
      { role: 'tool', content: '结果为 42', conversation: '数学讨论', process: true },
      { role: 'assistant', content: '答案是 42', conversation: '数学讨论' },
    ],
    empty: false,
  }) });
  await Promise.resolve();

  const children = runtime.elements.get('messages').children;
  assert.equal(children.length, 3);
  assert.equal(children[1].className, 'screen-process');
  assert.match(children[1].children[0].textContent, /2 条/);
  assert.equal(children[1].children[1].children.length, 2);
  assert.equal(children[1].open, undefined);
});

test('screen render errors are visible and are not acknowledged', () => {
  const runtime = screenRuntime();
  runtime.source.listeners.get('screen')({ data: '{invalid json' });

  assert.match(runtime.elements.get('connection').textContent, /渲染失败/);
  assert.equal(runtime.requests.length, 0);
});
