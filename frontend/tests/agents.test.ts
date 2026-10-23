import assert from 'node:assert/strict'
import test from 'node:test'
import { agentsForToolCalls, upsertAgent } from '../src/lib/agents.ts'
import type { AgentRun } from '../src/types.ts'

function run(agentId: string, callId: string, parentId?: string): AgentRun {
  return {
    agent_id: agentId, tool_call_id: callId, parent_agent_id: parentId,
    session_id: 'session', workspace_id: 'session', name: agentId,
    task: 'review', mode: 'read_only', model: 'model', status: 'running', created_at: '',
  }
}

test('each turn shows its own delegation and descendants, excluding truncated and other turns', () => {
  const agents = [
    run('grandchild', 'nested-call-2', 'child'),
    run('child', 'nested-call-1', 'current'),
    run('old', 'deleted-call'),
    run('another-turn', 'other-call'),
    run('current', 'current-call'),
  ]
  assert.deepEqual(agentsForToolCalls(agents, ['current-call']).map((agent) => agent.agent_id), ['grandchild', 'child', 'current'])
  assert.deepEqual(agentsForToolCalls(agents, []).map((agent) => agent.agent_id), [])
  assert.deepEqual(agentsForToolCalls(agents, ['other-call']).map((agent) => agent.agent_id), ['another-turn'])
})

test('replayed status updates replace an existing card without creating a duplicate', () => {
  const running = run('current', 'current-call')
  const completed = { ...running, status: 'completed' as const, result: 'verified' }
  const result = upsertAgent(upsertAgent([running], completed), completed)
  assert.equal(result.length, 1)
  assert.equal(result[0].status, 'completed')
  assert.equal(result[0].result, 'verified')
  assert.equal(running.status, 'running')
})
