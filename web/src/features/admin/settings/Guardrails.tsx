// Guardrails: settings over guardrails.* plus the recent audit events.
import { useState } from 'react'
import { useAdmin } from '../api'
import { Badge, Card, Loading, scrollCls, tdCls, theadCls, thCls } from '../ui'
import { SettingsForm, type FieldSpec } from './form'
import { Select } from '@/ui/Select'

const f = (key: string, kind: FieldSpec['kind'], hint?: string, label?: string): FieldSpec => ({ key, kind, hint, label })

const inputModes: [string, string][] = [
  ['off', 'Off'],
  ['log', 'Log only'],
  ['redact', 'Redact'],
  ['block', 'Block'],
]
const outputModes: [string, string][] = [
  ['off', 'Off'],
  ['log', 'Log only'],
  ['redact', 'Redact'],
]
const blockModes: [string, string][] = [
  ['off', 'Off'],
  ['log', 'Log only'],
  ['block', 'Block'],
]

interface GuardEvent {
  id: number
  user_id: string
  user_name: string
  user_email: string
  via_api_key: boolean
  model_id: string
  rule: string
  action: string
  count: number
  created_at: number
}
interface GuardSummary {
  rule: string
  action: string
  events: number
  hits: number
}

const ruleLabels: Record<string, string> = {
  'pii.email': 'Email address',
  'pii.phone': 'Phone number',
  'pii.card': 'Card number',
  'pii.pan': 'PAN',
  'pii.aadhaar': 'Aadhaar',
  'pii.iban': 'IBAN',
  'pii.ssn': 'SSN',
  injection: 'Prompt injection',
  blocklist: 'Blocklist',
  'moderation.unavailable': 'Moderation unavailable',
  'moderation.error': 'Moderation failed',
}

/** "output.pii.card" -> "Card number (reply)", "moderation.violence" -> "Moderation: violence". */
export function ruleLabel(rule: string): string {
  if (rule.startsWith('output.')) return `${ruleLabel(rule.slice(7))} (reply)`
  if (ruleLabels[rule]) return ruleLabels[rule]
  if (rule.startsWith('moderation.')) return `Moderation: ${rule.slice(11).replace(/[/_-]/g, ' ')}`
  return rule
}

const actionTone: Record<string, 'grey' | 'amber' | 'red'> = { log: 'grey', redact: 'amber', block: 'red' }

export function GuardrailsTab() {
  return (
    <div className="space-y-4">
      <SettingsForm
        sections={[
          {
            title: 'Input',
            hint: 'Checked on every message and tool result sent to a model, including background tasks and embeddings. Redacted values are replaced by placeholders like [card]; a blocked call fails with "guardrail_blocked".',
            fields: [
              f('guardrails.pii', { select: inputModes }, 'Email addresses, phone numbers, card numbers (Luhn-checked), PAN, Aadhaar, IBAN and SSN.', 'Personal data'),
              f('guardrails.injection', { select: blockModes }, 'Phrases that try to override the system prompt ("ignore previous instructions", "reveal your system prompt").', 'Prompt injection'),
              f('guardrails.blocklist', 'json', 'JSON list of case-insensitive regular expressions; a match in a user message always blocks the call.', 'Blocklist'),
            ],
          },
          {
            title: 'Reply',
            fields: [f('guardrails.output_pii', { select: outputModes }, 'Personal data in the model\'s reply. Redaction holds back a short tail of the stream so split values are still caught.', 'Personal data in replies')],
          },
          {
            title: 'Moderation',
            hint: 'Classifies the latest user message with a moderation model on the same connection (OpenAI /v1/moderations, or a gateway that routes one). Local models have none.',
            fields: [f('guardrails.moderation', { select: blockModes }, undefined, 'Moderation'), f('guardrails.moderation.model', 'text', undefined, 'Moderation model')],
          },
          {
            title: 'Scope',
            fields: [f('guardrails.local_exempt', 'bool', 'Skip every check for local (Ollama) models: nothing leaves the server. A model preset can override any of these with features.guardrails.', 'Exempt local models')],
          },
        ]}
      />
      <Events />
    </div>
  )
}

function Events() {
  const [days, setDays] = useState('7')
  const summary = useAdmin<GuardSummary[]>(['guardrails', 'summary', days], `/api/v1/admin/guardrails/summary?days=${days}`)
  const events = useAdmin<GuardEvent[]>(['guardrails', 'events', days], `/api/v1/admin/guardrails/events?days=${days}&limit=200`)
  return (
    <Card
      title="Recent events"
      hint="What the guardrails saw. Only rule names and counts are recorded, never the matched text."
      actions={
        <Select
          value={days}
          onChange={setDays}
          aria-label="Period"
          options={[
            { value: '1', label: 'Last day' },
            { value: '7', label: 'Last 7 days' },
            { value: '30', label: 'Last 30 days' },
            { value: '90', label: 'Last 90 days' },
          ]}
        />
      }
    >
      <Loading q={summary} />
      {summary.data && summary.data.length > 0 && (
        <div className="flex flex-wrap gap-2 border-b border-line px-5 py-3" data-testid="guardrail-summary">
          {summary.data.map((s) => (
            <Badge key={s.rule + s.action} tone={actionTone[s.action] ?? 'grey'} title={`${s.events} ${s.events === 1 ? 'event' : 'events'}`}>
              {ruleLabel(s.rule)} · {s.action} · {s.hits}
            </Badge>
          ))}
        </div>
      )}
      {events.data && events.data.length === 0 && <p className="px-5 py-6 text-sm text-muted">No events in this period.</p>}
      {events.data && events.data.length > 0 && (
        <div className={scrollCls}>
          <table className="w-full text-sm">
            <thead className={theadCls}>
              <tr>
                <th className={thCls}>When</th>
                <th className={thCls}>User</th>
                <th className={thCls}>Model</th>
                <th className={thCls}>Rule</th>
                <th className={thCls}>Action</th>
                <th className={thCls + ' text-right'}>Hits</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {events.data.map((e) => (
                <tr key={e.id} data-testid="guardrail-event">
                  <td className={tdCls + ' whitespace-nowrap text-muted'}>{new Date(e.created_at * 1000).toLocaleString()}</td>
                  <td className={tdCls}>
                    {e.user_name || e.user_email || <span className="text-muted">System</span>}
                    {e.via_api_key && (
                      <span className="ml-1 text-xs text-muted" title="Called through an API key">
                        (API key)
                      </span>
                    )}
                  </td>
                  <td className={tdCls}>
                    <code className="text-xs">{e.model_id}</code>
                  </td>
                  <td className={tdCls}>{ruleLabel(e.rule)}</td>
                  <td className={tdCls}>
                    <Badge tone={actionTone[e.action] ?? 'grey'}>{e.action}</Badge>
                  </td>
                  <td className={tdCls + ' text-right tabular-nums'}>{e.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}
