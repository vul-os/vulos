import { saveAs } from 'file-saver'
import pptxgen from 'pptxgenjs'
import DOMPurify from 'dompurify'

const PURIFY_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'input', 'button'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'onblur',
                'onchange', 'onsubmit', 'onkeydown', 'onkeyup', 'onkeypress'],
}

export function exportSlidesToPdf(filename) {
  const old = document.title
  document.title = filename
  window.print()
  document.title = old
}

export async function exportSlidesToPptx(data, filename) {
  const pres = new pptxgen()
  pres.layout = 'LAYOUT_WIDE'

  const THEME_COLORS = {
    black: { bg: '1a1a2e', fg: 'ffffff' },
    night: { bg: '282c34', fg: 'eeeeee' },
    dracula: { bg: '282a36', fg: 'f8f8f2' },
    white: { bg: 'ffffff', fg: '000000' },
    beige: { bg: 'f7f3de', fg: '333333' },
    sky: { bg: '87ceeb', fg: '333377' },
    solarized: { bg: 'fdf6e3', fg: '657b83' },
    serif: { bg: 'f0ece4', fg: '444444' },
    moon: { bg: '002b36', fg: 'aaaaaa' },
    league: { bg: '1c1e20', fg: 'eeeeee' },
  }

  const theme = THEME_COLORS[data.theme] || THEME_COLORS.black

  for (const slide of data.slides) {
    const s = pres.addSlide()
    s.background = { color: (slide.background?.replace('#', '') || theme.bg) }

    if (slide.title) {
      s.addText(slide.title, { x: 0.5, y: 0.5, w: '90%', h: 1, fontSize: 36, bold: true, color: theme.fg, fontFace: 'Calibri' })
    }

    const text = stripHtml(slide.content)
    if (text.trim()) {
      s.addText(text, {
        x: 0.5, y: slide.title ? 1.8 : 0.5, w: '90%', h: slide.title ? 4 : 5.5,
        fontSize: 20, color: theme.fg, fontFace: 'Calibri', valign: 'top', wrap: true,
      })
    }

    if (slide.notes) s.addNotes(slide.notes)
  }

  const blob = await pres.stream()
  saveAs(blob, `${filename}.pptx`)
}

function stripHtml(html) {
  const div = document.createElement('div')
  // Sanitize before DOM assignment to prevent XSS during text extraction.
  div.innerHTML = DOMPurify.sanitize(html ?? '', PURIFY_CONFIG)
  return div.textContent || div.innerText || ''
}
