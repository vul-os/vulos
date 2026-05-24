/**
 * InsertPanel.jsx — Insert toolbar: Image upload, Video embed, Shapes.
 *
 * Image:  upload via file input → api.uploadImage or base64 fallback.
 * Video:  paste URL → YouTube/Vimeo → iframe, or MP4 → <video>.
 *         Inserts an HTML string into the active Tiptap editor.
 * Shapes: basic SVG shapes inserted as HTML into the editor.
 *
 * Props:
 *   editor     Tiptap editor instance
 *   onInsert   (html) => void  — fallback when editor is not focused
 *   api        api module (for uploadImage)
 */

import { useRef, useState } from 'react'
import { Image as ImageIcon, Video, Square, Circle as CircleIcon, ArrowRight, X, Upload } from 'lucide-react'

const SHAPES = [
  {
    id: 'rect',
    label: 'Rectangle',
    icon: Square,
    html: (fill, stroke) => `<div contenteditable="false" style="display:inline-block;width:120px;height:80px;background:${fill};border:2px solid ${stroke};border-radius:4px;"></div>`,
  },
  {
    id: 'oval',
    label: 'Oval',
    icon: CircleIcon,
    html: (fill, stroke) => `<div contenteditable="false" style="display:inline-block;width:120px;height:80px;background:${fill};border:2px solid ${stroke};border-radius:50%;"></div>`,
  },
  {
    id: 'arrow',
    label: 'Arrow',
    icon: ArrowRight,
    html: (fill, _stroke) => `<div contenteditable="false" style="display:inline-block;font-size:48px;color:${fill};line-height:1;">&#8594;</div>`,
  },
]

function parseVideoUrl(url) {
  // YouTube
  const ytMatch = url.match(/(?:youtube\.com\/(?:watch\?v=|embed\/)|youtu\.be\/)([a-zA-Z0-9_-]{11})/)
  if (ytMatch) {
    return `<div contenteditable="false" style="position:relative;padding-bottom:56.25%;height:0;overflow:hidden;max-width:100%;">` +
      `<iframe style="position:absolute;top:0;left:0;width:100%;height:100%;" ` +
      `src="https://www.youtube.com/embed/${ytMatch[1]}" ` +
      `frameborder="0" allowfullscreen loading="lazy"></iframe></div>`
  }
  // Vimeo
  const vimeoMatch = url.match(/vimeo\.com\/(\d+)/)
  if (vimeoMatch) {
    return `<div contenteditable="false" style="position:relative;padding-bottom:56.25%;height:0;overflow:hidden;max-width:100%;">` +
      `<iframe style="position:absolute;top:0;left:0;width:100%;height:100%;" ` +
      `src="https://player.vimeo.com/video/${vimeoMatch[1]}" ` +
      `frameborder="0" allowfullscreen loading="lazy"></iframe></div>`
  }
  // MP4 / direct video URL
  if (/\.(mp4|webm|ogg)(\?|$)/i.test(url)) {
    return `<video controls style="max-width:100%;" loading="lazy">` +
      `<source src="${url}" /></video>`
  }
  return null
}

export default function InsertPanel({ editor, onInsert, api: apiProp }) {
  const imgInput = useRef(null)
  const [videoUrl, setVideoUrl] = useState('')
  const [videoErr, setVideoErr] = useState('')
  const [shapeFill, setShapeFill] = useState('#7c6af7')
  const [shapeStroke, setShapeStroke] = useState('#5b4dd0')
  const [showVideo, setShowVideo] = useState(false)
  const [showShape, setShowShape] = useState(false)

  const insertHtml = (html) => {
    if (editor) {
      editor.chain().focus().insertContent(html).run()
    } else {
      onInsert?.(html)
    }
  }

  const handleImageUpload = async (e) => {
    const file = e.target.files?.[0]
    if (!file) return
    try {
      const { url } = await apiProp.uploadImage(file)
      insertHtml(`<img src="${url}" alt="slide image" style="max-width:100%;" />`)
    } catch {
      const reader = new FileReader()
      reader.onload = (ev) => {
        if (ev.target?.result) {
          insertHtml(`<img src="${ev.target.result}" alt="slide image" style="max-width:100%;" />`)
        }
      }
      reader.readAsDataURL(file)
    }
    e.target.value = ''
  }

  const handleVideoInsert = () => {
    const trimmed = videoUrl.trim()
    if (!trimmed) { setVideoErr('Please enter a URL.'); return }
    const html = parseVideoUrl(trimmed)
    if (!html) { setVideoErr('Unrecognised URL. Paste a YouTube, Vimeo, or MP4 URL.'); return }
    insertHtml(html)
    setVideoUrl('')
    setVideoErr('')
    setShowVideo(false)
  }

  const handleShapeInsert = (shape) => {
    insertHtml(shape.html(shapeFill, shapeStroke))
    setShowShape(false)
  }

  return (
    <div className="flex items-center gap-1 flex-wrap">
      {/* Image */}
      <button
        type="button"
        title="Insert image"
        onClick={() => imgInput.current?.click()}
        className="inline-flex items-center gap-1 px-2 h-7 rounded-md border border-line text-xs text-ink-muted hover:text-ink hover:border-line-strong transition-colors"
      >
        <ImageIcon size={12} /> Image
      </button>
      <input ref={imgInput} type="file" accept="image/*" className="hidden" onChange={handleImageUpload} />

      {/* Video */}
      <div className="relative">
        <button
          type="button"
          title="Insert video"
          onClick={() => { setShowVideo((v) => !v); setShowShape(false) }}
          className={[
            'inline-flex items-center gap-1 px-2 h-7 rounded-md border text-xs transition-colors',
            showVideo
              ? 'border-accent bg-accent-tint text-accent'
              : 'border-line text-ink-muted hover:text-ink hover:border-line-strong',
          ].join(' ')}
        >
          <Video size={12} /> Video
        </button>
        {showVideo && (
          <div className="absolute top-full mt-1 left-0 z-20 bg-paper border border-line rounded-lg shadow-e2 p-3 w-72">
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold text-ink">Embed video</span>
              <button type="button" onClick={() => setShowVideo(false)} className="text-ink-faint hover:text-ink">
                <X size={12} />
              </button>
            </div>
            <input
              type="url"
              value={videoUrl}
              onChange={(e) => { setVideoUrl(e.target.value); setVideoErr('') }}
              onKeyDown={(e) => { if (e.key === 'Enter') handleVideoInsert() }}
              placeholder="YouTube, Vimeo, or MP4 URL"
              className="w-full text-xs bg-bg text-ink border border-line rounded-sm px-2 h-7 outline-none focus:border-accent"
              autoFocus
            />
            {videoErr && <p className="text-2xs text-danger mt-1">{videoErr}</p>}
            <p className="text-2xs text-ink-faint mt-1">
              Supports youtube.com, vimeo.com, and direct .mp4 links.
            </p>
            <button
              type="button"
              onClick={handleVideoInsert}
              className="mt-2 w-full text-xs bg-accent text-white rounded-md py-1.5 hover:bg-accent/90 transition-colors font-semibold"
            >
              Insert
            </button>
          </div>
        )}
      </div>

      {/* Shapes */}
      <div className="relative">
        <button
          type="button"
          title="Insert shape"
          onClick={() => { setShowShape((v) => !v); setShowVideo(false) }}
          className={[
            'inline-flex items-center gap-1 px-2 h-7 rounded-md border text-xs transition-colors',
            showShape
              ? 'border-accent bg-accent-tint text-accent'
              : 'border-line text-ink-muted hover:text-ink hover:border-line-strong',
          ].join(' ')}
        >
          <Square size={12} /> Shape
        </button>
        {showShape && (
          <div className="absolute top-full mt-1 left-0 z-20 bg-paper border border-line rounded-lg shadow-e2 p-3 w-56">
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold text-ink">Insert shape</span>
              <button type="button" onClick={() => setShowShape(false)} className="text-ink-faint hover:text-ink">
                <X size={12} />
              </button>
            </div>
            {/* Colour pickers */}
            <div className="flex gap-2 mb-2">
              <label className="flex items-center gap-1 text-2xs text-ink-muted">
                Fill
                <input
                  type="color"
                  value={shapeFill}
                  onChange={(e) => setShapeFill(e.target.value)}
                  className="w-6 h-6 rounded border border-line cursor-pointer"
                  style={{ padding: 1 }}
                />
              </label>
              <label className="flex items-center gap-1 text-2xs text-ink-muted">
                Stroke
                <input
                  type="color"
                  value={shapeStroke}
                  onChange={(e) => setShapeStroke(e.target.value)}
                  className="w-6 h-6 rounded border border-line cursor-pointer"
                  style={{ padding: 1 }}
                />
              </label>
            </div>
            <div className="grid grid-cols-3 gap-1.5">
              {SHAPES.map((shape) => {
                const Icon = shape.icon
                return (
                  <button
                    key={shape.id}
                    type="button"
                    onClick={() => handleShapeInsert(shape)}
                    className="flex flex-col items-center gap-1 py-2 rounded-md border border-line hover:border-accent hover:bg-accent-tint transition-colors text-ink-muted hover:text-accent"
                  >
                    <Icon size={16} />
                    <span className="text-2xs">{shape.label}</span>
                  </button>
                )
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
