// Minimal ambient declaration for the 'qrcode' package (no upstream types, and
// @types/qrcode isn't a dependency here — this file exists so the recovery-kit
// QR canvas in Setup.tsx can typecheck without adding a new package.json
// dependency). Deliberately narrow: only the API surface this codebase
// actually calls is declared, not the whole qrcode surface.
declare module 'qrcode' {
  export interface QRCodeToCanvasOptions {
    width?: number
    margin?: number
    color?: { dark?: string; light?: string }
    errorCorrectionLevel?: 'L' | 'M' | 'Q' | 'H'
  }

  export function toCanvas(
    canvas: HTMLCanvasElement,
    text: string,
    options?: QRCodeToCanvasOptions,
  ): Promise<void>

  const QRCode: {
    toCanvas: typeof toCanvas
  }
  export default QRCode
}
