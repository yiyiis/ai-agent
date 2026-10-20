import assert from 'node:assert/strict'
import test from 'node:test'
import { consumeStream, followStream } from '../src/api/stream.ts'
import type { StreamEvent, StreamOptions } from '../src/api/stream.ts'

const encoder = new TextEncoder()
const retryDelays = [0, 0, 0, 0] as const

function streamResponse(
  chunks: Array<string | Uint8Array>,
  readError?: Error,
): Response {
  let index = 0
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (index < chunks.length) {
        const chunk = chunks[index++]
        controller.enqueue(typeof chunk === 'string' ? encoder.encode(chunk) : chunk)
      } else if (readError) {
        controller.error(readError)
      } else {
        controller.close()
      }
    },
  })
  return new Response(body, { headers: { 'content-type': 'text/event-stream' } })
}

function frame(seq: number, event: StreamEvent): string {
  return `id: ${seq}\ndata: ${JSON.stringify(event)}\n\n`
}

function observe() {
  const events: StreamEvent[] = []
  const sequences: number[] = []
  return {
    events,
    sequences,
    onEvent: (event: StreamEvent) => events.push(event),
    onSeq: (seq: number) => sequences.push(seq),
  }
}

test('a reader failure reconnects from the last cursor without resending the message', async () => {
  const seen = observe()
  let starts = 0
  const attachedFrom: number[] = []
  const outcome = await followStream(
    async (onEvent, opts) => {
      starts++
      await consumeStream(streamResponse([
        'id: 7\n',
        'data: {"type":"delta","content":"A"}\n\n',
        'id: 8\ndata: {"type":"delta","content":"unfinished',
      ], new TypeError('network connection lost')), onEvent, opts)
    },
    async (fromSeq, onEvent, opts) => {
      attachedFrom.push(fromSeq)
      await consumeStream(streamResponse([
        frame(8, { type: 'delta', content: 'B' }),
        frame(9, { type: 'done', id: 'answer' }),
      ]), onEvent, opts)
      return true
    },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.equal(starts, 1)
  assert.deepEqual(attachedFrom, [8])
  assert.deepEqual(seen.sequences, [7, 8, 9])
  assert.deepEqual(seen.events, [
    { type: 'delta', content: 'A' },
    { type: 'delta', content: 'B' },
    { type: 'done', id: 'answer' },
  ])
})

test('failed reconnect attempts keep retrying and advance the replay cursor after partial recovery', async () => {
  const seen = observe()
  const attachedFrom: number[] = []
  const retries: Array<[number, number]> = []
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(
      streamResponse([], new TypeError('initial stream read failure')), onEvent, opts,
    ),
    async (fromSeq, onEvent, opts) => {
      attachedFrom.push(fromSeq)
      if (attachedFrom.length === 1) throw new TypeError('still offline')
      if (attachedFrom.length === 2) {
        await consumeStream(streamResponse([
          frame(0, { type: 'delta', content: 'recovered' }),
        ], new TypeError('connection lost again')), onEvent, opts)
      } else {
        await consumeStream(streamResponse([
          frame(1, { type: 'done', id: 'answer' }),
        ]), onEvent, opts)
      }
      return true
    },
    {
      signal: new AbortController().signal,
      ...seen,
      retryDelays,
      onRetry: (attempt, max) => retries.push([attempt, max]),
    },
  )

  assert.equal(outcome, 'terminal')
  assert.deepEqual(attachedFrom, [0, 0, 1])
  assert.deepEqual(retries, [[1, 4], [2, 4], [3, 4]])
  assert.deepEqual(seen.events, [
    { type: 'delta', content: 'recovered' },
    { type: 'done', id: 'answer' },
  ])
})

test('four failed reconnects report disconnected without synthesizing a terminal event', async () => {
  const seen = observe()
  let starts = 0
  let attaches = 0
  const outcome = await followStream(
    async (onEvent, opts) => {
      starts++
      await consumeStream(streamResponse([], new TypeError('offline')), onEvent, opts)
    },
    async () => {
      attaches++
      throw new TypeError('offline')
    },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'disconnected')
  assert.equal(starts, 1)
  assert.equal(attaches, 4)
  assert.deepEqual(seen.events, [])
})

test('a server SSE error is terminal and does not reconnect', async () => {
  const seen = observe()
  let attaches = 0
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(streamResponse([
      frame(0, { type: 'error', message: 'model request rejected' }),
    ]), onEvent, opts),
    async () => { attaches++; return true },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.equal(attaches, 0)
  assert.deepEqual(seen.events, [{ type: 'error', message: 'model request rejected' }])
})

test('a terminal event is retained even if the connection fails immediately afterward', async () => {
  const seen = observe()
  let attaches = 0
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(streamResponse([
      frame(0, { type: 'done', id: 'answer' }),
    ], new TypeError('socket closed')), onEvent, opts),
    async () => { attaches++; return true },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.equal(attaches, 0)
  assert.deepEqual(seen.events, [{ type: 'done', id: 'answer' }])
})

test('an unavailable replay (HTTP 204 mapped to false) stops reconnecting', async () => {
  const seen = observe()
  let attaches = 0
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(
      streamResponse([], new TypeError('network failure')), onEvent, opts,
    ),
    async () => { attaches++; return false },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'unavailable')
  assert.equal(attaches, 1)
  assert.deepEqual(seen.events, [])
})

test('EOF without a terminal event reconnects and resumes the existing turn', async () => {
  const seen = observe()
  const attachedFrom: number[] = []
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(streamResponse([
      frame(3, { type: 'delta', content: 'partial' }),
    ]), onEvent, opts),
    async (fromSeq, onEvent, opts) => {
      attachedFrom.push(fromSeq)
      await consumeStream(streamResponse([
        frame(4, { type: 'done', id: 'answer' }),
      ]), onEvent, opts)
      return true
    },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.deepEqual(attachedFrom, [4])
  assert.deepEqual(seen.sequences, [3, 4])
})

test('a confirmed SSE response with zero events reconnects from sequence zero', async () => {
  const seen = observe()
  const attachedFrom: number[] = []
  let starts = 0
  const outcome = await followStream(
    async (onEvent, opts) => {
      starts++
      await consumeStream(streamResponse([], new TypeError('read failed before first event')), onEvent, opts)
    },
    async (fromSeq, onEvent, opts) => {
      attachedFrom.push(fromSeq)
      await consumeStream(streamResponse([
        frame(0, { type: 'delta', content: 'new turn' }),
        frame(1, { type: 'done', id: 'new-answer' }),
      ]), onEvent, opts)
      return true
    },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.equal(starts, 1)
  assert.deepEqual(attachedFrom, [0])
  assert.deepEqual(seen.sequences, [0, 1])
  assert.deepEqual(seen.events, [
    { type: 'delta', content: 'new turn' },
    { type: 'done', id: 'new-answer' },
  ])
})

test('an unconfirmed initial request never attaches to a retained previous turn', async (t) => {
  const failedStarts = [
    {
      name: 'fetch rejects before response headers',
      start: async () => { throw new TypeError('failed to fetch') },
    },
    ...[408, 429, 502, 503].map((status) => ({
      name: `HTTP ${status} before SSE confirmation`,
      start: async (onEvent: (ev: StreamEvent) => void, opts: StreamOptions) => consumeStream(
        new Response('temporary failure', { status }), onEvent, opts,
      ),
    })),
    {
      name: 'non-SSE response before SSE confirmation',
      start: async (onEvent: (ev: StreamEvent) => void, opts: StreamOptions) => consumeStream(
        new Response('<html>gateway response</html>', {
          headers: { 'content-type': 'text/html' },
        }), onEvent, opts,
      ),
    },
  ]
  for (const { name, start } of failedStarts) {
    await t.test(name, async () => {
      const seen = observe()
      let attaches = 0
      let retries = 0
      const outcome = await followStream(
        start,
        async (_fromSeq, onEvent) => {
          attaches++
          onEvent({ type: 'delta', content: 'old retained turn' })
          onEvent({ type: 'done', id: 'old-answer' })
          return true
        },
        {
          signal: new AbortController().signal,
          ...seen,
          retryDelays,
          onRetry: () => { retries++ },
        },
      )

      assert.equal(outcome, 'disconnected')
      assert.equal(attaches, 0)
      assert.equal(retries, 0)
      assert.deepEqual(seen.events, [])
    })
  }
})

test('aborting during retry backoff stops promptly without a subsequent attach', async () => {
  const controller = new AbortController()
  const seen = observe()
  let attaches = 0
  let notifyRetry!: () => void
  const retryStarted = new Promise<void>((resolve) => { notifyRetry = resolve })
  const pending = followStream(
    async (onEvent, opts) => consumeStream(
      streamResponse([], new TypeError('offline')), onEvent, opts,
    ),
    async () => { attaches++; return true },
    {
      signal: controller.signal,
      ...seen,
      retryDelays: [60_000],
      onRetry: notifyRetry,
    },
  )

  await retryStarted
  controller.abort()
  const outcome = await pending
  assert.equal(outcome, 'aborted')
  assert.equal(attaches, 0)
  assert.deepEqual(seen.events, [])
})

test('an already aborted signal neither starts nor attaches', async () => {
  const controller = new AbortController()
  controller.abort()
  let calls = 0
  const outcome = await followStream(
    async () => { calls++ },
    async () => { calls++; return true },
    { signal: controller.signal, onEvent: () => {}, onSeq: () => {}, retryDelays },
  )

  assert.equal(outcome, 'aborted')
  assert.equal(calls, 0)
})

test('SSE IDs and Unicode data survive arbitrary network chunk boundaries', async () => {
  const seen = observe()
  const payload = encoder.encode([
    ': ping\r\n\r\n',
    'id: 20\r\ndata: {"type":"delta","content":"你"}\r\n\r\n',
    'id: 21\r\ndata: {"type":"delta","content":"好"}\r\n\r\n',
    'id: 22\r\ndata: {"type":"done","id":"answer"}\r\n\r\n',
  ].join(''))
  const chunks = Array.from(payload, (byte) => Uint8Array.of(byte))
  await consumeStream(streamResponse(chunks), seen.onEvent, { onSeq: seen.onSeq })

  assert.deepEqual(seen.sequences, [20, 21, 22])
  assert.deepEqual(seen.events, [
    { type: 'delta', content: '你' },
    { type: 'delta', content: '好' },
    { type: 'done', id: 'answer' },
  ])
})

test('transport HTTP failures reject without dispatching a business error', async (t) => {
  for (const status of [408, 429, 500, 502, 503, 504]) {
    await t.test(`HTTP ${status}`, async () => {
      const seen = observe()
      await assert.rejects(consumeStream(
        new Response('temporary failure', { status }),
        seen.onEvent,
      ))
      assert.deepEqual(seen.events, [])
    })
  }
})

test('an unexpected non-SSE response rejects without dispatching a business error', async () => {
  const seen = observe()
  await assert.rejects(consumeStream(
    new Response('<html>gateway unavailable</html>', {
      headers: { 'content-type': 'text/html' },
    }),
    seen.onEvent,
  ))
  assert.deepEqual(seen.events, [])
})

test('non-retryable HTTP client failures dispatch a terminal error', async (t) => {
  for (const status of [400, 401, 403, 404, 409, 422]) {
    await t.test(`HTTP ${status}`, async () => {
      const seen = observe()
      let attaches = 0
      const outcome = await followStream(
        async (onEvent, opts: StreamOptions) => consumeStream(
          new Response('request rejected', { status }), onEvent, opts,
        ),
        async () => { attaches++; return true },
        { signal: new AbortController().signal, ...seen, retryDelays },
      )
      assert.equal(outcome, 'terminal')
      assert.equal(attaches, 0)
      assert.equal(seen.events.length, 1)
      assert.equal(seen.events[0].type, 'error')
    })
  }
})

test('a JSON business error retains the server message and is terminal', async () => {
  const seen = observe()
  let attaches = 0
  const outcome = await followStream(
    async (onEvent, opts) => consumeStream(new Response(
      JSON.stringify({ code: 1001, msg: '会话不存在', data: null }),
      { headers: { 'content-type': 'application/json' } },
    ), onEvent, opts),
    async () => { attaches++; return true },
    { signal: new AbortController().signal, ...seen, retryDelays },
  )

  assert.equal(outcome, 'terminal')
  assert.equal(attaches, 0)
  assert.deepEqual(seen.events, [{ type: 'error', message: '会话不存在' }])
})
