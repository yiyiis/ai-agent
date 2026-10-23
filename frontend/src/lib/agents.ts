import type { AgentRun } from '../types'

/** Only show agents whose delegation still belongs to this turn, including descendants. */
export function agentsForToolCalls(agents: AgentRun[], toolCallIds: string[]): AgentRun[] {
  const calls = new Set(toolCallIds)
  const selected = new Set(agents.filter((agent) => calls.has(agent.tool_call_id)).map((agent) => agent.agent_id))
  let expanded = true
  while (expanded) {
    expanded = false
    for (const agent of agents) {
      if (agent.parent_agent_id && selected.has(agent.parent_agent_id) && !selected.has(agent.agent_id)) {
        selected.add(agent.agent_id)
        expanded = true
      }
    }
  }
  return agents.filter((agent) => selected.has(agent.agent_id))
}

export function upsertAgent(agents: AgentRun[], incoming: AgentRun): AgentRun[] {
  const index = agents.findIndex((agent) => agent.agent_id === incoming.agent_id)
  if (index < 0) return [...agents, incoming]
  return agents.map((agent, i) => i === index ? incoming : agent)
}
