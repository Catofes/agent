const test = require('node:test')
const assert = require('node:assert/strict')
const { createParser } = require('./ndjson-stream.js')

test('one network chunk can contain multiple NDJSON events', () => {
  const events = []
  const parser = createParser(event => events.push(event))
  const bytes = new TextEncoder().encode(
    '{"type":"text_delta","delta":"你"}\n' +
    '{"type":"text_delta","delta":"好"}\n' +
    '{"type":"turn_end","reason":"completed"}\n'
  )

  parser.push(bytes, false)
  parser.push(undefined, true)

  assert.deepEqual(events.map(event => event.type), ['text_delta', 'text_delta', 'turn_end'])
  assert.equal(events[0].delta + events[1].delta, '你好')
})

test('JSON and Chinese UTF-8 may span arbitrary chunks', () => {
  const events = []
  const parser = createParser(event => events.push(event))
  const bytes = new TextEncoder().encode(
    '{"type":"reasoning_delta","delta":"先想"}\n' +
    '{"type":"text_delta","delta":"中文"}'
  )

  for (const byte of bytes) parser.push(Uint8Array.of(byte), false)
  parser.push(undefined, true)

  assert.deepEqual(events, [
    { type: 'reasoning_delta', delta: '先想' },
    { type: 'text_delta', delta: '中文' }
  ])
})
