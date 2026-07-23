package gateway

// origin_bridge.go — ORIGIN-01: the shell↔app postMessage bridge (app side).
//
// Once apps stop running on the shell's origin they can no longer reach the
// shell's storage — which is the point. The five first-party apps that used to
// need `allow-same-origin` needed it for exactly one reason: they read
// localStorage during initial script execution, and localStorage THROWS in an
// opaque origin (white-screening the app). They get that capability back here,
// mediated, over a narrow RPC surface.
//
// TRANSPORT. The gateway injects the client below into every app HTML response.
// It:
//
//  1. Seeds a synchronous localStorage shim from a snapshot the shell puts in the
//     iframe URL fragment. The fragment is chosen deliberately: it is the only
//     channel that is available SYNCHRONOUSLY at first script execution (a
//     postMessage handshake is inherently async, and the apps read at load), and
//     it never leaves the browser — fragments are not sent to the server and not
//     logged. The shim therefore behaves like the real localStorage for the read-
//     at-boot pattern the apps actually use.
//
//  2. Opens a MessageChannel and hands the shell one end via a SINGLE postMessage
//     whose targetOrigin is the shell's exact origin. This is the app-side origin
//     check, and it is enforced by the browser: if the frame's parent is not the
//     shell origin, the message (and the port with it) is simply never delivered,
//     so the bridge fails closed. `*` is never used as a targetOrigin.
//
//  3. Sends all subsequent traffic over that MessagePort. A port is point-to-point
//     and unforgeable — only the holder of the other end can send on it — so no
//     origin comparison is needed (or possible) on port traffic, and no third
//     party can inject into it. The client installs NO window-level `message`
//     listener at all, so a hostile frame cannot even address the app.
//
// CAPABILITY SURFACE (v1) — deliberately tiny, and enumerated in full:
//
//	app → shell   vulos.bridge.hello       handshake; carries the MessagePort
//	app → shell   vulos.storage.set        {key, value}   namespaced to this app
//	app → shell   vulos.storage.remove     {key}          namespaced to this app
//	app → shell   vulos.storage.clear      (this app's namespace only)
//	shell → app   vulos.storage.snapshot   {data}         this app's keys only
//	app → shell   vulos.offline.state      {id} → reply {unlocked}   (OFFLINE-AUTH-01)
//	app → shell   vulos.offline.appKey     {id} → reply {key}  this app's CryptoKey
//
// The app-facing surface is exposed as window.vulos.offline.{isUnlocked,appKey}
// (frozen). appKey() returns a non-extractable CryptoKey the shell scopes to THIS
// app from the trusted frame identity — an app can never name or reach another
// app's key.
//
// That is the whole protocol. There is no shell→app command channel, no DOM
// access, no navigation, no capability to read another app's data: the shell
// derives the app's identity from the FRAME, never from the message, and scopes
// every key to it (see src/core/AppBridge.js).

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// requestScheme returns the scheme the browser used to reach us.
//
// X-Forwarded-Proto is honoured ONLY from a loopback peer, mirroring the trust
// rule already used for session cookies (services/auth/handlers.go) and for
// X-Forwarded-For: a remote client must not be able to talk us into minting
// https origins for a plaintext connection, since the origin string we emit is
// what the shell and the app compare against.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if ip := net.ParseIP(strings.Trim(remoteHost, "[]")); ip != nil && ip.IsLoopback() {
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			return "https"
		}
	}
	return "http"
}

// injectIntoHead splices head content into an HTML document as early as possible
// — the bridge must run before the app's own scripts, because those scripts read
// localStorage during initial execution and the shim has to already be in place.
func injectIntoHead(html, head string) string {
	if head == "" {
		return html
	}
	lower := strings.ToLower(html)
	if idx := strings.Index(lower, "<head>"); idx >= 0 {
		return html[:idx+6] + head + html[idx+6:]
	}
	if idx := strings.Index(lower, "<html"); idx >= 0 {
		if end := strings.Index(html[idx:], ">"); end >= 0 {
			pos := idx + end + 1
			return html[:pos] + "<head>" + head + "</head>" + html[pos:]
		}
	}
	return head + html
}

// jsStringLiteral renders s as a safe JS double-quoted string literal. The only
// value we interpolate is an origin we built ourselves from a validated host, but
// escaping it is not optional: this string is spliced into a <script> in an HTML
// document, so an unescaped `</script>` or quote would be an injection.
func jsStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '<' || r == '>' || r == '&':
			// Escaped as \uXXXX so a literal "</script>" in the value can never
			// terminate the <script> element we are splicing this into.
			fmt.Fprintf(&b, `\u%04x`, r)
		case r == '\u2028' || r == '\u2029':
			// JS line terminators: legal in HTML, illegal raw inside a string literal.
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// bridgeScript renders the injected <script> for an app frame. shellOrigin is the
// exact origin the app must address; when it is empty the app is not framable by
// anyone we can name and NO bridge is installed (fail closed — an app with no
// bridge simply has no persistent storage; it does not get a looser one).
func bridgeScript(shellOrigin string) string {
	if shellOrigin == "" {
		return ""
	}
	return `<script>(function(){"use strict";
var SHELL=` + jsStringLiteral(shellOrigin) + `,V=1;
if(window.top===window.self)return;            // not framed: nothing to bridge
var store=Object.create(null),port=null,pending=[],reqId=0,waiting=Object.create(null);

// (1) Synchronous seed from the URL fragment (never sent to the server).
try{
  var m=/(?:^|[#&])__vulos_s=([^&]*)/.exec(window.location.hash||"");
  if(m&&m[1]){
    var bin=atob(decodeURIComponent(m[1])),bytes=new Uint8Array(bin.length);
    for(var i=0;i<bin.length;i++)bytes[i]=bin.charCodeAt(i);
    var seed=JSON.parse(new TextDecoder().decode(bytes));
    if(seed&&typeof seed==="object")for(var k in seed){if(typeof seed[k]==="string")store[k]=seed[k];}
  }
}catch(e){}
// Hide our seed from the app so it cannot be mistaken for the app's own hash
// state. Throws in an opaque origin on some engines — best effort.
try{if(window.location.hash.indexOf("__vulos_s=")>=0)history.replaceState(null,"",window.location.pathname+window.location.search);}catch(e){}

function send(m){m.v=V;if(port){try{port.postMessage(m);}catch(e){}}else{pending.push(m);}}

// request/response over the same private port (OFFLINE-AUTH-01). Each call gets a
// fresh id; the shell echoes it on the reply. Resolves null on timeout so a
// missing shell never hangs an app.
function request(type){return new Promise(function(resolve){var id=++reqId;var to=setTimeout(function(){if(waiting[id]){delete waiting[id];resolve(null);}},5000);waiting[id]=function(v){clearTimeout(to);resolve(v);};send({type:type,id:id});});}

// (2) localStorage shim. Reads are served from the seeded snapshot (synchronous,
// exactly like the real thing); writes are applied locally and mirrored to the
// shell, which owns the durable copy under this app's namespace.
var shim={
  getItem:function(k){k=String(k);return Object.prototype.hasOwnProperty.call(store,k)?store[k]:null;},
  setItem:function(k,v){k=String(k);v=String(v);store[k]=v;send({type:"vulos.storage.set",key:k,value:v});},
  removeItem:function(k){k=String(k);delete store[k];send({type:"vulos.storage.remove",key:k});},
  clear:function(){store=Object.create(null);send({type:"vulos.storage.clear"});},
  key:function(i){var ks=Object.keys(store);i=i>>>0;return i<ks.length?ks[i]:null;}
};
Object.defineProperty(shim,"length",{get:function(){return Object.keys(store).length;}});
try{Object.defineProperty(window,"localStorage",{value:shim,configurable:true});}catch(e){}
try{Object.defineProperty(window,"sessionStorage",{value:shim,configurable:true});}catch(e){}

// (3) Handshake: hand the shell one end of a private channel, addressed to its
// EXACT origin. If our parent is not that origin the browser drops this message
// and the port is never delivered — the bridge fails closed, by construction.
// All shell->app traffic arrives on the port, which is unforgeable; the app
// installs no window-level message listener, so nothing else can reach it.
try{
  var ch=new MessageChannel();
  ch.port1.onmessage=function(ev){
    var msg=ev.data;
    if(!msg||msg.v!==V)return;
    if(msg.type==="vulos.storage.snapshot"&&msg.data&&typeof msg.data==="object"){
      for(var k in msg.data){if(typeof msg.data[k]==="string"&&!(k in store))store[k]=msg.data[k];}
    }
    else if((msg.type==="vulos.offline.state.reply"||msg.type==="vulos.offline.appKey.reply")&&waiting[msg.id]){
      var w=waiting[msg.id];delete waiting[msg.id];
      w(msg.type==="vulos.offline.state.reply"?!!msg.unlocked:(msg.key||null));
    }
  };
  port=ch.port1;
  window.parent.postMessage({type:"vulos.bridge.hello",v:V},SHELL,[ch.port2]);
  for(var j=0;j<pending.length;j++){try{port.postMessage(pending[j]);}catch(e){}}
  pending.length=0;
}catch(e){}

// (4) OFFLINE-AUTH-01: the app-facing offline API. An app gates its cached UI on
// isUnlocked() and encrypts its cache with appKey() (a non-extractable CryptoKey
// scoped to THIS app by the shell, from the trusted frame identity — the app
// cannot name another app). Read-only, minimal surface.
try{
  Object.defineProperty(window,"vulos",{value:Object.freeze({offline:Object.freeze({
    isUnlocked:function(){return request("vulos.offline.state");},
    appKey:function(){return request("vulos.offline.appKey");}
  })}),configurable:false,writable:false});
}catch(e){}
})();</script>`
}
