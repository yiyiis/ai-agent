import test from 'node:test'
import assert from 'node:assert/strict'
import { parseInputQuestion, parseInputResponse } from '../src/lib/requestInput.ts'

test('partial streamed input is not a ready question', () => {
  for (const raw of ['', '{}', 'null', '{"question":', '{"question":"ok","options":[null]}']) {
    assert.equal(parseInputQuestion(raw), null)
  }
  assert.deepEqual(parseInputQuestion('{"question":"格式？","options":["PDF","Word"]}'), {
    question: '格式？', options: ['PDF', 'Word'],
  })
})

test('answer, skip, and stop stay distinguishable after reload', () => {
  assert.equal(parseInputResponse('{"status":"answered","answer":"PDF"}')?.answer, 'PDF')
  assert.equal(parseInputResponse('{"status":"cancelled","action":"skip"}')?.action, 'skip')
  assert.equal(parseInputResponse('{"status":"cancelled","action":"stop"}')?.action, 'stop')
  assert.equal(parseInputResponse('[参数解析失败]'), null)
})
