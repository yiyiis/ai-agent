export function parseInputQuestion(raw: string): { question: string; options?: string[] } | null {
  try {
    const value = JSON.parse(raw)
    if (!value || typeof value.question !== 'string' || !value.question.trim()) return null
    if (value.options !== undefined && (!Array.isArray(value.options) || !value.options.every((v: unknown) => typeof v === 'string'))) return null
    return value
  } catch { return null }
}

export function parseInputResponse(raw?: string): { status: 'answered' | 'cancelled'; answer?: string; action?: string } | null {
  try {
    const value = JSON.parse(raw || '')
    if (!value || !['answered', 'cancelled'].includes(value.status)) return null
    if (value.answer !== undefined && typeof value.answer !== 'string') return null
    return value
  } catch { return null }
}
