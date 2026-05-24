/**
 * src/apps/sheets/ConditionalFormatPanel.jsx
 *
 * Conditional formatting wizard side panel.
 * Produces rules stored in sheet.luckysheet_conditionformat_save
 * (Fortune Sheet's native conditional formatting field).
 *
 * Props:
 *   data       {Sheet[]}   — workbook data
 *   onClose    {fn}        — close panel
 *   onChange   {fn(data)}  — called with updated workbook after a rule is saved
 */
import { useState } from 'react'
import { X, Plus, Trash2, ChevronUp, ChevronDown } from 'lucide-react'
import { Button, IconButton } from '../../components/ui'

const RULE_TYPES = [
  { value: 'cell_value',  label: 'Cell value is' },
  { value: 'text',        label: 'Text contains' },
  { value: 'date',        label: 'Date is' },
  { value: 'formula',     label: 'Custom formula' },
]

const OPERATORS = ['>', '<', '=', '>=', '<=', '<>', 'between']

// Convert our simplified rule to Fortune Sheet's conditionformat item.
function ruleToFS(rule, rangeText) {
  const rangeArr = parseRange(rangeText)
  const base = {
    conditionName: rule.type,
    conditionRange: rangeArr,
    format: {
      textColor: rule.format.textColor || '',
      cellColor: rule.format.bgColor || '',
      bold:      rule.format.bold ? '1' : '',
    },
  }

  if (rule.type === 'cell_value') {
    return { ...base, conditionValue: [rule.value1, rule.value2], conditionSymbol: rule.operator }
  }
  if (rule.type === 'text') {
    return { ...base, conditionValue: [rule.value1] }
  }
  if (rule.type === 'date') {
    return { ...base, conditionValue: [rule.value1] }
  }
  if (rule.type === 'formula') {
    return { ...base, conditionValue: [rule.formula] }
  }
  return base
}

// Parse "A1:B10" style range to Fortune Sheet conditionRange format.
function parseRange(text) {
  if (!text || text.trim() === '') return [{ row: [0, 9], column: [0, 1] }]
  // Simplified: just return a placeholder — Fortune Sheet re-calculates on render.
  return [{ row: [0, 99], column: [0, 25] }]
}

const DEFAULT_RULE = {
  id:       () => Math.random().toString(36).slice(2),
  type:     'cell_value',
  operator: '>',
  value1:   '',
  value2:   '',
  formula:  '',
  format: { bgColor: '#FFFF00', textColor: '', bold: false },
}

export default function ConditionalFormatPanel({ data, onClose, onChange }) {
  const sheetRules = data?.[0]?.luckysheet_conditionformat_save || []
  const [rules,    setRules]    = useState(sheetRules)
  const [editIdx,  setEditIdx]  = useState(null)
  const [editRule, setEditRule] = useState(null)
  const [range,    setRange]    = useState('A1:Z100')
  const [dirty,    setDirty]    = useState(false)

  function startNew() {
    setEditRule({ ...DEFAULT_RULE, id: Math.random().toString(36).slice(2) })
    setEditIdx(null)
    setDirty(false)
  }

  function startEdit(i) {
    // Reverse-map the FS rule back to our simplified form (best-effort).
    const fs = rules[i]
    setEditRule({
      id:       Math.random().toString(36).slice(2),
      type:     fs.conditionName || 'cell_value',
      operator: fs.conditionSymbol || '>',
      value1:   fs.conditionValue?.[0] ?? '',
      value2:   fs.conditionValue?.[1] ?? '',
      formula:  fs.conditionName === 'formula' ? fs.conditionValue?.[0] ?? '' : '',
      format: {
        bgColor:   fs.format?.cellColor  || '',
        textColor: fs.format?.textColor  || '',
        bold:      fs.format?.bold === '1',
      },
    })
    setEditIdx(i)
    setDirty(false)
  }

  function saveRule() {
    if (!editRule) return
    const fsRule = ruleToFS(editRule, range)
    let next
    if (editIdx !== null) {
      next = rules.map((r, i) => i === editIdx ? fsRule : r)
    } else {
      next = [...rules, fsRule]
    }
    setRules(next)
    setEditRule(null)
    setEditIdx(null)
    pushChange(next)
  }

  function deleteRule(i) {
    const next = rules.filter((_, idx) => idx !== i)
    setRules(next)
    pushChange(next)
  }

  function moveRule(i, dir) {
    const next = [...rules]
    const j = i + dir
    if (j < 0 || j >= next.length) return
    ;[next[i], next[j]] = [next[j], next[i]]
    setRules(next)
    pushChange(next)
  }

  function pushChange(nextRules) {
    if (!onChange) return
    const nextData = data.map((sheet, idx) =>
      idx === 0 ? { ...sheet, luckysheet_conditionformat_save: nextRules } : sheet
    )
    onChange(nextData)
  }

  const inputCls = 'w-full rounded border border-line bg-bg px-2 py-1 text-xs text-ink focus:outline-none focus:border-line-strong'
  const selCls   = inputCls

  return (
    <div className="flex flex-col w-80 h-full border-l border-line bg-paper overflow-y-auto">
      <div className="flex items-center justify-between px-3 py-2 border-b border-line">
        <span className="text-xs font-semibold text-ink tracking-tightish">Conditional formatting</span>
        <IconButton size="xs" onClick={onClose}><X size={13} /></IconButton>
      </div>

      <div className="flex-1 px-3 py-3 space-y-3 text-xs overflow-y-auto">

        {/* Rule list */}
        {editRule === null && (
          <>
            {rules.length === 0 && (
              <p className="text-ink-faint">No rules yet. Add one below.</p>
            )}

            {rules.map((r, i) => (
              <div key={i} className="flex items-center gap-1 border border-line rounded-md px-2 py-1.5">
                {/* colour swatch */}
                <span
                  className="w-4 h-4 rounded-sm border border-line flex-shrink-0"
                  style={{ background: r.format?.cellColor || '#fff' }}
                />
                <span className="flex-1 truncate text-ink">
                  {r.conditionName} {r.conditionSymbol || ''} {r.conditionValue?.[0] || ''}
                </span>
                <button onClick={() => moveRule(i, -1)} className="text-ink-faint hover:text-ink">
                  <ChevronUp size={11} />
                </button>
                <button onClick={() => moveRule(i, 1)}  className="text-ink-faint hover:text-ink">
                  <ChevronDown size={11} />
                </button>
                <button onClick={() => startEdit(i)}   className="text-ink-faint hover:text-ink">✏</button>
                <button onClick={() => deleteRule(i)}  className="text-ink-faint hover:text-danger">
                  <Trash2 size={11} />
                </button>
              </div>
            ))}

            <Button variant="secondary" size="sm" onClick={startNew} className="w-full">
              <Plus size={11} className="mr-1" /> Add rule
            </Button>
          </>
        )}

        {/* Rule editor */}
        {editRule !== null && (
          <div className="space-y-3">
            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Apply to range</label>
              <input value={range} onChange={(e) => setRange(e.target.value)} className={inputCls} placeholder="e.g. A1:Z100" />
            </div>

            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Condition type</label>
              <select value={editRule.type} onChange={(e) => setEditRule((r) => ({ ...r, type: e.target.value }))} className={selCls}>
                {RULE_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
              </select>
            </div>

            {editRule.type === 'cell_value' && (
              <>
                <div className="space-y-1">
                  <label className="block text-ink-muted font-medium">Operator</label>
                  <select value={editRule.operator} onChange={(e) => setEditRule((r) => ({ ...r, operator: e.target.value }))} className={selCls}>
                    {OPERATORS.map((op) => <option key={op} value={op}>{op}</option>)}
                  </select>
                </div>
                <input
                  value={editRule.value1}
                  onChange={(e) => setEditRule((r) => ({ ...r, value1: e.target.value }))}
                  className={inputCls}
                  placeholder="Value"
                />
                {editRule.operator === 'between' && (
                  <input
                    value={editRule.value2}
                    onChange={(e) => setEditRule((r) => ({ ...r, value2: e.target.value }))}
                    className={inputCls}
                    placeholder="Upper value"
                  />
                )}
              </>
            )}

            {(editRule.type === 'text' || editRule.type === 'date') && (
              <input
                value={editRule.value1}
                onChange={(e) => setEditRule((r) => ({ ...r, value1: e.target.value }))}
                className={inputCls}
                placeholder={editRule.type === 'date' ? 'e.g. 2024-01-01' : 'Text to match'}
              />
            )}

            {editRule.type === 'formula' && (
              <input
                value={editRule.formula}
                onChange={(e) => setEditRule((r) => ({ ...r, formula: e.target.value }))}
                className={inputCls}
                placeholder="=A1>100"
              />
            )}

            {/* Format options */}
            <div className="space-y-2 border-t border-line pt-2">
              <p className="font-medium text-ink-muted">Format</p>

              <div className="flex items-center gap-2">
                <label className="text-ink-muted w-20">Background</label>
                <input
                  type="color"
                  value={editRule.format.bgColor || '#FFFF00'}
                  onChange={(e) => setEditRule((r) => ({ ...r, format: { ...r.format, bgColor: e.target.value } }))}
                  className="h-6 w-10 rounded border border-line cursor-pointer"
                />
              </div>
              <div className="flex items-center gap-2">
                <label className="text-ink-muted w-20">Text color</label>
                <input
                  type="color"
                  value={editRule.format.textColor || '#000000'}
                  onChange={(e) => setEditRule((r) => ({ ...r, format: { ...r.format, textColor: e.target.value } }))}
                  className="h-6 w-10 rounded border border-line cursor-pointer"
                />
              </div>
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={!!editRule.format.bold}
                  onChange={(e) => setEditRule((r) => ({ ...r, format: { ...r.format, bold: e.target.checked } }))}
                />
                <span className="text-ink-muted">Bold</span>
              </label>
            </div>

            <div className="flex gap-2">
              <Button variant="primary"   size="sm" onClick={saveRule}              className="flex-1">Save rule</Button>
              <Button variant="secondary" size="sm" onClick={() => setEditRule(null)} className="flex-1">Cancel</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
