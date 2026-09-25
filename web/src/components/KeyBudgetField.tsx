import { Field, Input } from './ui'

export default function KeyBudgetField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <Field label="USD spend limit" hint="Uses the team's UTC budget period. Blank or zero removes only this key's cap; other limits still apply. Changing the cap does not reset usage. Capped keys require priced chat, embeddings or Responses; unmetered routes are denied. Chat defaults to a 1024-token output ceiling when omitted.">
    <Input type="text" inputMode="decimal" maxLength={20} value={value} onChange={e => onChange(e.target.value)} placeholder="e.g. 25.00" />
  </Field>
}
