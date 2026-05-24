/**
 * src/apps/sheets/ChartWizard.jsx
 *
 * Chart insertion wizard modal.
 * Fortune Sheet has built-in chart support via the `chart` config on a sheet.
 * This wizard builds a Fortune Sheet chart descriptor and appends it.
 *
 * Props:
 *   data      {Sheet[]}   — workbook data
 *   onClose   {fn}        — close wizard
 *   onChange  {fn(data)}  — called with updated data after insertion
 */
import { useState } from 'react'
import { X, BarChart2 } from 'lucide-react'
import { Button, IconButton } from '../../components/ui'

const CHART_TYPES = [
  { value: 'bar',    label: 'Bar',    icon: '▬' },
  { value: 'column', label: 'Column', icon: '▮' },
  { value: 'line',   label: 'Line',   icon: '╱' },
  { value: 'pie',    label: 'Pie',    icon: '◔' },
  { value: 'scatter',label: 'Scatter',icon: '⊡' },
  { value: 'area',   label: 'Area',   icon: '△' },
]

const LEGEND_POSITIONS = ['top', 'bottom', 'left', 'right', 'none']

/**
 * Build a Fortune Sheet chart descriptor.
 * Fortune Sheet v1 stores charts as chart_id objects in sheet.chart (array).
 * We produce a minimal valid descriptor that Fortune Sheet can render.
 */
function buildChartDescriptor({ type, range, title, xAxisLabel, yAxisLabel, legendPos, colors }) {
  return {
    chart_id:    'chart_' + Math.random().toString(36).slice(2),
    width:       600,
    height:      400,
    left:        60,
    top:         60,
    sheetIndex:  0,
    needRangeShow: true,
    rangeArray:  [{ row: [0, 9], column: [0, 3] }],
    rangeColCheck: { exits: false, range: [] },
    rangeRowCheck: { exits: false, range: [] },
    rangeConfigCheck: { exits: false, range: '' },
    defaultRange: '',
    chartOptions: {
      chart_type:  type,
      title:       { value: title, show: !!title },
      xAxis:       { title: { value: xAxisLabel, show: !!xAxisLabel } },
      yAxis:       { title: { value: yAxisLabel, show: !!yAxisLabel } },
      legend:      { show: legendPos !== 'none', position: legendPos },
      colors:      colors || [],
      rangeConfig: range,
    },
  }
}

export default function ChartWizard({ data, onClose, onChange }) {
  const [step,        setStep]        = useState(0) // 0=type, 1=config
  const [chartType,   setChartType]   = useState('column')
  const [range,       setRange]       = useState('')
  const [title,       setTitle]       = useState('')
  const [xLabel,      setXLabel]      = useState('')
  const [yLabel,      setYLabel]      = useState('')
  const [legendPos,   setLegendPos]   = useState('bottom')

  function handleInsert() {
    const chart = buildChartDescriptor({
      type:        chartType,
      range,
      title,
      xAxisLabel:  xLabel,
      yAxisLabel:  yLabel,
      legendPos,
    })
    const nextData = data.map((sheet, idx) => {
      if (idx !== 0) return sheet
      const existing = sheet.chart || []
      return { ...sheet, chart: [...existing, chart] }
    })
    onChange(nextData)
    onClose()
  }

  const inputCls = 'w-full rounded border border-line bg-bg px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-line-strong'
  const selCls   = inputCls

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-paper rounded-xl border border-line shadow-e4 w-[480px] max-h-[80vh] flex flex-col overflow-hidden animate-scale-in">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-line">
          <span className="text-sm font-semibold text-ink flex items-center gap-2">
            <BarChart2 size={14} className="text-accent" /> Insert chart
          </span>
          <IconButton size="xs" onClick={onClose}><X size={13} /></IconButton>
        </div>

        <div className="flex-1 overflow-y-auto px-4 py-4 space-y-5">
          {/* Step 0 — chart type */}
          {step === 0 && (
            <div className="space-y-3">
              <p className="text-xs text-ink-muted font-medium">Select chart type</p>
              <div className="grid grid-cols-3 gap-2">
                {CHART_TYPES.map((ct) => (
                  <button
                    key={ct.value}
                    onClick={() => setChartType(ct.value)}
                    className={[
                      'flex flex-col items-center gap-1.5 py-3 rounded-lg border text-xs',
                      'transition-colors duration-fast',
                      chartType === ct.value
                        ? 'border-accent bg-accent-tint text-accent font-semibold'
                        : 'border-line text-ink-muted hover:border-line-strong hover:text-ink',
                    ].join(' ')}
                  >
                    <span className="text-xl leading-none">{ct.icon}</span>
                    {ct.label}
                  </button>
                ))}
              </div>
            </div>
          )}

          {/* Step 1 — customise */}
          {step === 1 && (
            <div className="space-y-4 text-xs">
              <div className="space-y-1">
                <label className="block text-ink-muted font-medium">Data range</label>
                <input
                  value={range}
                  onChange={(e) => setRange(e.target.value)}
                  className={inputCls}
                  placeholder="e.g. A1:D10"
                />
              </div>

              <div className="space-y-1">
                <label className="block text-ink-muted font-medium">Chart title</label>
                <input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  className={inputCls}
                  placeholder="Optional title"
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1">
                  <label className="block text-ink-muted font-medium">X-axis label</label>
                  <input value={xLabel} onChange={(e) => setXLabel(e.target.value)} className={inputCls} placeholder="X axis" />
                </div>
                <div className="space-y-1">
                  <label className="block text-ink-muted font-medium">Y-axis label</label>
                  <input value={yLabel} onChange={(e) => setYLabel(e.target.value)} className={inputCls} placeholder="Y axis" />
                </div>
              </div>

              <div className="space-y-1">
                <label className="block text-ink-muted font-medium">Legend position</label>
                <select value={legendPos} onChange={(e) => setLegendPos(e.target.value)} className={selCls}>
                  {LEGEND_POSITIONS.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-4 py-3 border-t border-line gap-2">
          {step === 0 ? (
            <>
              <Button variant="secondary" size="sm" onClick={onClose}>Cancel</Button>
              <Button variant="primary" size="sm" onClick={() => setStep(1)}>Next →</Button>
            </>
          ) : (
            <>
              <Button variant="secondary" size="sm" onClick={() => setStep(0)}>← Back</Button>
              <Button variant="primary" size="sm" onClick={handleInsert}>Insert chart</Button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
