const BASE = '/api'

async function request(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...options.headers }

  // Session is managed via an httpOnly cookie set by the backend on login.
  // credentials: 'include' ensures the browser sends it automatically.
  const res = await fetch(BASE + path, { ...options, headers, credentials: 'include' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw Object.assign(new Error(err.error || 'Request failed'), err)
  }
  return res.json()
}

export const api = {
  authStatus: () => request('/auth/status'),
  login: (password) =>
    request('/auth/login', { method: 'POST', body: JSON.stringify({ password }) }),
  logout: () =>
    request('/auth/logout', { method: 'POST' }),

  listFiles: () => request('/files'),
  getFile: (id) => request(`/files/${id}`),
  createFile: (name, type, content) =>
    request('/files', { method: 'POST', body: JSON.stringify({ name, type, content }) }),
  updateFile: (id, name, content) =>
    request(`/files/${id}`, { method: 'PUT', body: JSON.stringify({ name, content }) }),
  deleteFile: (id) =>
    request(`/files/${id}`, { method: 'DELETE' }),

  // OFFICE-08: version history
  listVersions: (id) => request(`/files/${id}/versions`),
  restoreVersion: (id, vid) =>
    request(`/files/${id}/versions/${vid}/restore`, { method: 'POST' }),

  // OFFICE-28: activity feed + named snapshots
  getActivity: (id) => request(`/files/${id}/activity`),
  createNamedSnapshot: (id, label) =>
    request(`/files/${id}/versions`, { method: 'POST', body: JSON.stringify({ label }) }),
  labelVersion: (id, vid, label) =>
    request(`/files/${id}/versions/${vid}/label`, { method: 'PUT', body: JSON.stringify({ label }) }),

  // OFFICE-27: suggestions / track-changes
  listSuggestions: (fileId) => request(`/files/${fileId}/suggestions`),
  createSuggestion: (fileId, kind, authorId, from, to, text) =>
    request(`/files/${fileId}/suggestions`, {
      method: 'POST',
      body: JSON.stringify({ kind, author_id: authorId, from, to, text }),
    }),
  updateSuggestion: (fileId, suggestionId, state, reviewerId = '') =>
    request(`/files/${fileId}/suggestions/${suggestionId}`, {
      method: 'PUT',
      body: JSON.stringify({ state, reviewer_id: reviewerId }),
    }),
  deleteSuggestion: (fileId, suggestionId) =>
    request(`/files/${fileId}/suggestions/${suggestionId}`, { method: 'DELETE' }),

  // OFFICE-26: comments (anchored, threaded, resolvable)
  listComments: (fileId) => request(`/files/${fileId}/comments`),
  createComment: (fileId, anchor, authorId, body) =>
    request(`/files/${fileId}/comments`, { method: 'POST', body: JSON.stringify({ anchor, author_id: authorId, body }) }),
  updateComment: (fileId, commentId, patch) =>
    request(`/files/${fileId}/comments/${commentId}`, { method: 'PUT', body: JSON.stringify(patch) }),
  deleteComment: (fileId, commentId) =>
    request(`/files/${fileId}/comments/${commentId}`, { method: 'DELETE' }),
  createReply: (fileId, commentId, authorId, body) =>
    request(`/files/${fileId}/comments/${commentId}/replies`, { method: 'POST', body: JSON.stringify({ author_id: authorId, body }) }),
  updateReply: (fileId, commentId, replyId, patch) =>
    request(`/files/${fileId}/comments/${commentId}/replies/${replyId}`, { method: 'PUT', body: JSON.stringify(patch) }),
  deleteReply: (fileId, commentId, replyId) =>
    request(`/files/${fileId}/comments/${commentId}/replies/${replyId}`, { method: 'DELETE' }),

  scanLocalFiles: () => request('/local-files'),
  localFileUrl: (path) => `${BASE}/local-files/serve?path=${encodeURIComponent(path)}`,

  // OFFICE-60/61: Vulos Spaces API
  spacesListChannels: () => request('/spaces/channels'),
  spacesCreateChannel: (name, type, members = []) =>
    request('/spaces/channels', { method: 'POST', body: JSON.stringify({ name, type, members }) }),
  spacesJoinChannel: (channelId) =>
    request(`/spaces/channels/${channelId}/join`, { method: 'POST' }),
  spacesListMembers: (channelId) => request(`/spaces/channels/${channelId}/members`),
  spacesListMessages: (channelId) => request(`/spaces/channels/${channelId}/messages`),
  spacesSendMessage: (channelId, body, threadParent = '') =>
    request(`/spaces/channels/${channelId}/messages`, {
      method: 'POST',
      body: JSON.stringify({ body, thread_parent: threadParent }),
    }),
  spacesEditMessage: (channelId, msgId, body) =>
    request(`/spaces/channels/${channelId}/messages/${msgId}`, {
      method: 'PUT',
      body: JSON.stringify({ body }),
    }),
  spacesDeleteMessage: (channelId, msgId) =>
    request(`/spaces/channels/${channelId}/messages/${msgId}`, { method: 'DELETE' }),
  spacesMarkRead: (channelId, clock) =>
    request(`/spaces/channels/${channelId}/read`, {
      method: 'POST',
      body: JSON.stringify({ clock }),
    }),
  spacesGetReadState: (channelId) => request(`/spaces/channels/${channelId}/read`),
  spacesExportOps: (channelId, afterClock = '') =>
    request(`/spaces/channels/${channelId}/ops${afterClock ? `?after=${encodeURIComponent(afterClock)}` : ''}`),

  // Reactions
  spacesListReactions: (channelId) => request(`/spaces/channels/${channelId}/reactions`),
  spacesReact: (channelId, msgId, emoji) =>
    request(`/spaces/messages/${msgId}/react`, {
      method: 'POST',
      body: JSON.stringify({ emoji, channel_id: channelId }),
    }),
  spacesUnreact: (channelId, msgId, emoji) =>
    request(`/spaces/messages/${msgId}/react`, {
      method: 'DELETE',
      body: JSON.stringify({ emoji, channel_id: channelId }),
    }),

  // Pins
  spacesPinsList: (channelId) => request(`/spaces/channels/${channelId}/pins`),
  spacesPinMessage: (channelId, msgId) =>
    request(`/spaces/channels/${channelId}/pins`, {
      method: 'POST',
      body: JSON.stringify({ message_id: msgId }),
    }),
  spacesUnpinMessage: (channelId, msgId) =>
    request(`/spaces/channels/${channelId}/pins/${msgId}`, { method: 'DELETE' }),

  // User status
  spacesSetStatus: (status, customText, untilUnix = 0) =>
    request('/spaces/users/me/status', {
      method: 'PUT',
      body: JSON.stringify({ status, custom_text: customText, until_unix: untilUnix }),
    }),

  // Channel search
  spacesSearch: (channelId, q) =>
    request(`/spaces/channels/${channelId}/search?q=${encodeURIComponent(q)}`),

  // OFFICE-41: signing envelope CRUD
  listEnvelopes: () => request('/envelopes'),
  getEnvelope: (id) => request(`/envelopes/${id}`),
  createEnvelope: (env) =>
    request('/envelopes', { method: 'POST', body: JSON.stringify(env) }),
  updateEnvelope: (id, env) =>
    request(`/envelopes/${id}`, { method: 'PUT', body: JSON.stringify(env) }),
  deleteEnvelope: (id) =>
    request(`/envelopes/${id}`, { method: 'DELETE' }),

  // OFFICE-45: orchestration — status, remind, cancel, decline
  envelopeStatus: (envelopeId) => request(`/sign/${envelopeId}/status`),
  envelopeRemind: (envelopeId) =>
    request(`/sign/${envelopeId}/remind`, { method: 'POST', body: '{}' }),
  envelopeCancel: (envelopeId) =>
    request(`/sign/${envelopeId}/cancel`, { method: 'POST', body: '{}' }),
  signerDecline: (token) =>
    request(`/sign/${token}/decline`, { method: 'POST', body: '{}' }),

  // Docs export: returns a Blob for download (PDF or DOCX)
  exportDoc: async (fileId, format) => {
    const res = await fetch(`${BASE}/files/${fileId}/export?format=${encodeURIComponent(format)}`, {
      credentials: 'include',
    })
    if (!res.ok) throw new Error(`Export failed: ${res.statusText}`)
    return res.blob()
  },

  uploadImage: async (file) => {
    const form = new FormData()
    form.append('file', file)
    // Cookie sent automatically via credentials: 'include'.
    const res = await fetch(BASE + '/upload', { method: 'POST', body: form, credentials: 'include' })
    if (!res.ok) throw new Error('Upload failed')
    return res.json()
  },
}
