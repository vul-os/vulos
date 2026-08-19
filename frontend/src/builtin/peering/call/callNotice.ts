// callNotice.ts — the words a user gets when a peer calls and nothing here can
// answer.
//
// It lives in its own module rather than beside the component for two reasons:
// a component file that also exports helpers breaks fast refresh
// (react-refresh/only-export-components), and this text is the FIX. The
// incoming-call surface no longer offers an Answer button it would refuse; what
// replaced it is a decline on the wire plus this notice. If the notice goes
// silent, a call vanishes with no trace, which is worse than the ringing card
// this replaced — so the text is separately importable and separately tested.

/**
 * What the user is told when a peer calls.
 *
 * It says "no client in this build", NOT "calling is unavailable". The box's
 * signalling relay is complete and working — initiate/answer/reject/signal/
 * hangup, mesh, lobby and ICE are all live and unit-tested. What is missing is
 * the browser side, retired on purpose in ef3e3175. Blaming the box would send
 * a user hunting for a fault in hardware that is fine.
 */
export function declinedCallNotice(who: string): { title: string; body: string } {
  return {
    title: `Missed call from ${who}`,
    body: `${who} tried to call you over Vulos. Peer-to-peer calling has no client in this ` +
      `build, so there is nothing here to answer with and the call was declined rather than ` +
      `left ringing. Reach them by phone or on whichever call app you both use.`,
  }
}
