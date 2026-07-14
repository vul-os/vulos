/**
 * spdx.mjs — identify a licence from the text of the licence file itself.
 *
 * Used by both gen-notices.mjs and vendor-apps.mjs so the two can never label
 * the same file differently.
 *
 * Rules that matter here:
 *  - We only return an id when the text says so. An unrecognised text returns
 *    null and the caller prints "see text below" — a wrong label is worse than
 *    no label, because a wrong label is what a compliance audit reads.
 *  - Titles are matched against the HEAD of the file, not the whole body. The
 *    MPL-2.0 text names the GPL, LGPL and AGPL in its own §1.12 definition of a
 *    "Secondary License"; a whole-body search for "GNU Affero" therefore labels
 *    every MPL-2.0 package as AGPL-3.0. (It did: lightningcss, which is MPL-2.0,
 *    came out AGPL-3.0 until this was fixed.)
 */

const HEAD_CHARS = 600

/** @returns {string|null} an SPDX id, or null when the text is not recognised. */
export function detectSPDX(text) {
  const norm = text.replace(/\s+/g, ' ').trim()
  const head = norm.slice(0, HEAD_CHARS)

  // Title-line families: these licences announce themselves at the top.
  if (/GNU AFFERO GENERAL PUBLIC LICENSE\b.{0,40}Version 3/i.test(head)) return 'AGPL-3.0'
  if (/GNU LESSER GENERAL PUBLIC LICENSE\b.{0,40}Version 3/i.test(head)) return 'LGPL-3.0'
  if (/GNU LESSER GENERAL PUBLIC LICENSE\b.{0,40}Version 2\.1/i.test(head)) return 'LGPL-2.1'
  if (/GNU GENERAL PUBLIC LICENSE\b.{0,40}Version 3/i.test(head)) return 'GPL-3.0'
  if (/GNU GENERAL PUBLIC LICENSE\b.{0,40}Version 2/i.test(head)) return 'GPL-2.0'
  if (/Mozilla Public License[, ]+(Version|v\.?) ?2\.0/i.test(head)) return 'MPL-2.0'
  if (/Apache License\b.{0,40}Version 2\.0/i.test(head)) return 'Apache-2.0'
  if (/SIL OPEN FONT LICENSE/i.test(head)) return 'OFL-1.1'

  // Short permissive licences: identified by their operative sentence, which can
  // sit below a copyright header of any length.
  if (/Permission is hereby granted, free of charge, to any person obtaining a copy/i.test(norm)) return 'MIT'
  if (/Permission to use, copy, modify, and(\/or)? distribute this software for any purpose with or without fee/i.test(norm)) {
    // ISC and 0BSD share this sentence. ISC keeps the notice-retention proviso;
    // 0BSD drops it. (tslib is 0BSD and was being reported as ISC.)
    return /provided that the above copyright notice and this permission notice appear in all copies/i.test(norm)
      ? 'ISC'
      : '0BSD'
  }
  if (/Redistribution and use in source and binary forms/i.test(norm)) {
    if (/Neither the name/i.test(norm)) return 'BSD-3-Clause'
    return 'BSD-2-Clause'
  }
  if (/This is free and unencumbered software released into the public domain/i.test(norm)) return 'Unlicense'

  // A file that only says "Licensed under the Apache License, Version 2.0" with
  // no title (some NOTICE-style files) — accept it, but only after the title
  // families above have had their chance.
  if (/Licensed under the Apache License, Version 2\.0/i.test(norm)) return 'Apache-2.0'

  return null
}

/** Licence files, by conventional filename. */
export const LICENSE_FILENAMES = /^(licen[cs]e|copying|notice)([-._].*)?(\.(txt|md|rst))?$/i
