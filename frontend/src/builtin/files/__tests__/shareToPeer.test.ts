import { describe, it, expect } from 'vitest'
import {
  resolveAbsPath, guessMime, buildSendBody, splitPath, buildFolderArchiveCommand,
} from '../shareToPeer.js'

describe('resolveAbsPath', () => {
  it('expands ~ to the resolved home', () => {
    expect(resolveAbsPath('~/Documents/a.txt', '/home/vulos')).toBe('/home/vulos/Documents/a.txt')
    expect(resolveAbsPath('~', '/home/vulos')).toBe('/home/vulos')
  })
  it('leaves absolute paths untouched', () => {
    expect(resolveAbsPath('/tmp/x.txt', '/home/vulos')).toBe('/tmp/x.txt')
  })
  it('falls back to the raw path when home is unknown', () => {
    expect(resolveAbsPath('~/x', null)).toBe('~/x')
  })
})

describe('guessMime', () => {
  it('maps known extensions', () => {
    expect(guessMime('report.pdf')).toBe('application/pdf')
    expect(guessMime('pic.PNG')).toBe('image/png')
    expect(guessMime('clip.mp4')).toBe('video/mp4')
  })
  it('defaults to octet-stream for unknown / extensionless', () => {
    expect(guessMime('data.unknownext')).toBe('application/octet-stream')
    expect(guessMime('Makefile')).toBe('application/octet-stream')
    expect(guessMime('')).toBe('application/octet-stream')
  })
})

describe('buildSendBody', () => {
  it('matches the drop/send contract and includes target_addr when known', () => {
    const body = buildSendBody(
      { vulos_id: 'vulos:abc', addr: '192.168.1.5:8080' },
      '/home/vulos/a.txt', 'text/plain',
    )
    expect(body).toEqual({
      target_vulos_id: 'vulos:abc',
      media_path: '/home/vulos/a.txt',
      mime_type: 'text/plain',
      target_addr: 'http://192.168.1.5:8080',
    })
  })
  it('omits target_addr when the peer has no advertised address', () => {
    const body = buildSendBody({ vulos_id: 'vulos:abc' }, '/x', '')
    expect(body.target_addr).toBeUndefined()
    expect(body.mime_type).toBe('application/octet-stream')
  })
})

describe('splitPath', () => {
  it('splits nested paths', () => {
    expect(splitPath('/home/vulos/Docs')).toEqual({ parent: '/home/vulos', base: 'Docs' })
  })
  it('handles root-level entries', () => {
    expect(splitPath('/etc')).toEqual({ parent: '/', base: 'etc' })
  })
})

describe('buildFolderArchiveCommand', () => {
  it('tars the folder into a temp .tar.gz keeping the folder name', () => {
    const { command, archivePath } = buildFolderArchiveCommand('/home/vulos/Photos')
    expect(archivePath).toMatch(/^\/tmp\/\.vulos-share-\d+\/Photos\.tar\.gz$/)
    expect(command).toContain('tar -czf')
    expect(command).toContain('-C "/home/vulos" "Photos"')
    expect(command).toContain('echo OK')
  })
})
