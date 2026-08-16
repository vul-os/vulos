package proctl

// A minimal X11 core-protocol client — enough of it to send one _NET_WM_PING
// and read the echo, and nothing more.
//
// # Why this is spoken rather than shelled out
//
// The rootfs ships xdotool (scripts/build-sh-packages.txt) and the container
// image installs it too (Dockerfile), so "there is no X client on the box" is
// no longer the obstacle. But xdotool cannot do this job, and neither can xprop
// or xwininfo, and the reason is the whole point of the feature:
//
//	Every one of those tools asks the X SERVER about a window. The server holds
//	the window tree, the properties and the geometry, and it answers from its
//	own memory whether or not the client that owns the window is still alive. An
//	application whose event loop is wedged solid still has a window with a name,
//	a size and a full property list, so `xdotool search --name` and
//	`xprop -id N` both succeed against it and look exactly like health.
//
// That is the shape of this project's recorded worst gate: a placement check
// that read `foot --title` while the shipping client was `cog`, which sets no
// title — a proxy signal wearing the clothes of a measurement. A window query
// standing in for a liveness probe would be the same mistake with a different
// tool.
//
// _NET_WM_PING is the one exchange in X where the CLIENT, not the server, must
// act: the ping is delivered to the application's own event queue, and the
// echo only exists if the application dequeued it and sent one back. No X tool
// in Debian sends one — it is a window-manager function, and matchbox (the WM
// this box runs on the Xvfb path) does not expose it. Sending it needs an X
// client that can wait for a reply on the root window, which means speaking the
// protocol. It is ~300 lines and no new dependency; the alternative was adding
// an X binding to a module that has none for one message.
//
// Scope: connection setup, InternAtom, QueryTree, GetProperty,
// ChangeWindowAttributes, SendEvent and GetInputFocus. No extensions, no
// drawing, no auth beyond the empty one. This is deliberately not the beginning
// of an X library.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// X11 request opcodes used here. proto(7) numbering.
const (
	opChangeWindowAttributes = 2
	opQueryTree              = 15
	opInternAtom             = 16
	opGetProperty            = 20
	opSendEvent              = 25
	opGetInputFocus          = 43
)

// Event mask bits. Only SubstructureNotify is ever requested.
//
// SubstructureRedirect (0x100000) is deliberately NOT requested and must never
// be: the server grants it to ONE client at a time, and that client is the
// window manager. Asking for it would either fail with BadAccess or, worse,
// succeed on a display whose WM has not started yet and silently make this
// probe the window manager.
const eventMaskSubstructureNotify = 0x00080000

// cwEventMask is the ChangeWindowAttributes value-mask bit for event-mask.
const cwEventMask = 0x00000800

// X11 packet type discriminators. Byte 0 of every 32-byte packet: 0 is an
// error, 1 is a reply, anything else is an event code.
const (
	pktError = 0
	pktReply = 1
)

// evClientMessage is the event code for ClientMessage.
const evClientMessage = 33

// sentBit marks an event that arrived via SendEvent rather than being generated
// by the server. The _NET_WM_PING echo ALWAYS has it set, because the
// application sends it — reading byte 0 without masking this off is a
// straightforward way to never match the reply this file exists to read.
const sentBit = 0x80

// atomWindow and atomAtom are predefined atoms (proto(7) appendix B).
const (
	atomAtom uint32 = 4
)

// maxReplyWords bounds a reply body at 4 MiB. A GetProperty against a hostile
// or broken client could otherwise name any length and have this allocate it.
const maxReplyWords = 1 << 20

// xconn is one connection to an X server.
type xconn struct {
	c    net.Conn
	root uint32
	// seq mirrors the server's request counter. The server stamps every reply
	// and error with the sequence number of the request it belongs to, and
	// replies arrive in request order; tracking it is what lets an error for an
	// earlier no-reply request (ChangeWindowAttributes, SendEvent) be noticed
	// instead of silently swallowed while waiting for a later reply.
	seq uint16
	// events holds events that arrived while waiting for a reply. The ping echo
	// can legitimately land there — the app may answer before the round-trip
	// that follows the send completes — so they are queued, never dropped.
	events []xpacket
}

// xpacket is one 32-byte X packet plus any reply body.
type xpacket struct {
	kind byte // 0 error, 1 reply, >=2 event code (sentBit already masked off)
	seq  uint16
	data []byte
}

// xdial connects to an X server over its unix socket and completes the
// connection setup.
//
// The authorisation is empty. That is correct for this caller and only this
// caller: services/stream starts Xvfb with -ac and no -auth file, so the server
// has no authorisation records and accepts the empty protocol. A display
// started any other way will refuse the connection, which surfaces as an
// explicit setup failure rather than as a wrong verdict.
func xdial(ctx context.Context, socket string) (*xconn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	x := &xconn{c: c}
	if err := x.setup(); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

func (x *xconn) Close() { x.c.Close() }

// setup performs the connection handshake and records the first screen's root
// window, which is the only thing from it this probe needs.
func (x *xconn) setup() error {
	req := make([]byte, 12)
	req[0] = 'l' // LSB-first; every platform Vulos ships to is little-endian
	binary.LittleEndian.PutUint16(req[2:], 11)
	binary.LittleEndian.PutUint16(req[4:], 0)
	// auth-protocol-name-length and auth-protocol-data-length stay 0.
	if _, err := x.c.Write(req); err != nil {
		return err
	}

	head := make([]byte, 8)
	if _, err := io.ReadFull(x.c, head); err != nil {
		return fmt.Errorf("setup reply: %w", err)
	}
	words := binary.LittleEndian.Uint16(head[6:8])
	body := make([]byte, int(words)*4)
	if _, err := io.ReadFull(x.c, body); err != nil {
		return fmt.Errorf("setup body: %w", err)
	}
	switch head[0] {
	case 0:
		n := int(head[1])
		if n > len(body) {
			n = len(body)
		}
		return fmt.Errorf("X server refused the connection: %s", string(body[:n]))
	case 2:
		return errors.New("X server demands authorisation and this probe offers none")
	}

	// Layout from proto(7): the fixed part runs to byte 39 of the whole reply
	// (byte 31 of body), then the vendor string padded to 4, then 8 bytes per
	// pixmap format, then the screens. The root window is the first field of
	// the first screen.
	if len(body) < 32 {
		return errors.New("setup reply truncated")
	}
	vendorLen := int(binary.LittleEndian.Uint16(body[16:18]))
	numScreens := int(body[20])
	numFormats := int(body[21])
	if numScreens < 1 {
		return errors.New("X server reports no screens")
	}
	off := 32 + pad4(vendorLen) + 8*numFormats
	if off+4 > len(body) {
		return errors.New("setup reply does not contain a screen")
	}
	x.root = binary.LittleEndian.Uint32(body[off : off+4])
	return nil
}

// pad4 rounds n up to a multiple of 4, the padding every X string and property
// value carries.
func pad4(n int) int { return (n + 3) &^ 3 }

// send writes one request and returns the sequence number the server will stamp
// on its reply or error.
func (x *xconn) send(req []byte) (uint16, error) {
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return 0, err
	}
	return x.seq, nil
}

// readPacket reads exactly one packet.
func (x *xconn) readPacket() (xpacket, error) {
	head := make([]byte, 32)
	if _, err := io.ReadFull(x.c, head); err != nil {
		return xpacket{}, err
	}
	p := xpacket{kind: head[0] &^ sentBit, seq: binary.LittleEndian.Uint16(head[2:4]), data: head}
	if head[0] == pktReply {
		words := binary.LittleEndian.Uint32(head[4:8])
		if words > maxReplyWords {
			return xpacket{}, fmt.Errorf("reply claims %d words", words)
		}
		if words > 0 {
			extra := make([]byte, int(words)*4)
			if _, err := io.ReadFull(x.c, extra); err != nil {
				return xpacket{}, err
			}
			p.data = append(p.data, extra...)
		}
	}
	return p, nil
}

// xerror is a protocol error from the server.
type xerror struct {
	code byte
	seq  uint16
}

func (e *xerror) Error() string { return fmt.Sprintf("X error %d on request %d", e.code, e.seq) }

// awaitReply reads until the reply for seq arrives, queueing events and
// surfacing errors — including errors for EARLIER requests, which is the only
// way a failed ChangeWindowAttributes or SendEvent is ever heard about, since
// neither has a reply of its own.
func (x *xconn) awaitReply(seq uint16) ([]byte, error) {
	for {
		p, err := x.readPacket()
		if err != nil {
			return nil, err
		}
		switch {
		case p.kind >= 2:
			x.events = append(x.events, p)
		case p.kind == pktError && seqLE(p.seq, seq):
			return nil, &xerror{code: p.data[1], seq: p.seq}
		case p.kind == pktReply && p.seq == seq:
			return p.data, nil
		}
	}
}

// seqLE compares X sequence numbers, which are 16-bit and wrap. Requests within
// one probe number in the dozens, so "a is at most a little before b" is the
// only comparison needed and a half-range window is ample.
func seqLE(a, b uint16) bool { return b-a < 1<<15 }

// roundTrip issues a GetInputFocus purely for its reply.
//
// This is the standard X idiom for "has the server processed everything I sent,
// and is it still there?" — it is tiny, has no side effects and cannot fail
// with a protocol error. It is used here for something the feature depends on:
// after an unanswered ping, it is what separates "the application did not
// answer" from "the X server stopped answering", and those two have different
// names, different causes and different remedies.
func (x *xconn) roundTrip() error {
	req := make([]byte, 4)
	req[0] = opGetInputFocus
	binary.LittleEndian.PutUint16(req[2:], 1)
	seq, err := x.send(req)
	if err != nil {
		return err
	}
	_, err = x.awaitReply(seq)
	return err
}

// internAtom resolves an atom name.
func (x *xconn) internAtom(name string) (uint32, error) {
	n := len(name)
	req := make([]byte, 8+pad4(n))
	req[0] = opInternAtom
	req[1] = 0 // only-if-exists = false
	binary.LittleEndian.PutUint16(req[2:], uint16(len(req)/4))
	binary.LittleEndian.PutUint16(req[4:], uint16(n))
	copy(req[8:], name)
	seq, err := x.send(req)
	if err != nil {
		return 0, err
	}
	data, err := x.awaitReply(seq)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(data[8:12]), nil
}

// queryTree returns a window's children.
func (x *xconn) queryTree(win uint32) ([]uint32, error) {
	req := make([]byte, 8)
	req[0] = opQueryTree
	binary.LittleEndian.PutUint16(req[2:], 2)
	binary.LittleEndian.PutUint32(req[4:], win)
	seq, err := x.send(req)
	if err != nil {
		return nil, err
	}
	data, err := x.awaitReply(seq)
	if err != nil {
		return nil, err
	}
	if len(data) < 32 {
		return nil, errors.New("QueryTree reply truncated")
	}
	n := int(binary.LittleEndian.Uint16(data[16:18]))
	if 32+4*n > len(data) {
		return nil, errors.New("QueryTree reply shorter than its child count")
	}
	kids := make([]uint32, 0, n)
	for i := 0; i < n; i++ {
		kids = append(kids, binary.LittleEndian.Uint32(data[32+4*i:]))
	}
	return kids, nil
}

// getProperty reads up to 1024 32-bit units of a property. Returns the value
// bytes and the property's type atom; a missing property is (nil, 0, nil) —
// absence is an ordinary answer here, not an error.
func (x *xconn) getProperty(win, prop, typ uint32) ([]byte, uint32, error) {
	req := make([]byte, 24)
	req[0] = opGetProperty
	binary.LittleEndian.PutUint16(req[2:], 6)
	binary.LittleEndian.PutUint32(req[4:], win)
	binary.LittleEndian.PutUint32(req[8:], prop)
	binary.LittleEndian.PutUint32(req[12:], typ)
	binary.LittleEndian.PutUint32(req[16:], 0)    // long-offset
	binary.LittleEndian.PutUint32(req[20:], 1024) // long-length
	seq, err := x.send(req)
	if err != nil {
		return nil, 0, err
	}
	data, err := x.awaitReply(seq)
	if err != nil {
		// A BadWindow here means the window died between QueryTree and now,
		// which is normal on a live display and must not abort the walk.
		var xe *xerror
		if errors.As(err, &xe) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if len(data) < 32 {
		return nil, 0, errors.New("GetProperty reply truncated")
	}
	format := data[1]
	ptype := binary.LittleEndian.Uint32(data[8:12])
	units := int(binary.LittleEndian.Uint32(data[16:20]))
	if ptype == 0 || format == 0 {
		return nil, 0, nil
	}
	n := units * int(format) / 8
	if 32+n > len(data) {
		n = len(data) - 32
	}
	return data[32 : 32+n], ptype, nil
}

// selectRootSubstructure asks for SubstructureNotify on the root window, which
// is where a pinged application sends its echo.
//
// ChangeWindowAttributes has no reply, so the caller must follow this with a
// round trip for any error to be seen.
func (x *xconn) selectRootSubstructure() error {
	req := make([]byte, 16)
	req[0] = opChangeWindowAttributes
	binary.LittleEndian.PutUint16(req[2:], 4)
	binary.LittleEndian.PutUint32(req[4:], x.root)
	binary.LittleEndian.PutUint32(req[8:], cwEventMask)
	binary.LittleEndian.PutUint32(req[12:], eventMaskSubstructureNotify)
	_, err := x.send(req)
	return err
}

// sendClientMessage delivers a 32-byte ClientMessage to dest.
//
// mask is 0 for a ping: an empty event-mask means "deliver to the client that
// CREATED the destination window", which is precisely the addressing this needs
// — the message must land in the application's own queue, not be broadcast to
// whoever is watching the window.
func (x *xconn) sendClientMessage(dest uint32, mask uint32, ev []byte) error {
	if len(ev) != 32 {
		return errors.New("event must be 32 bytes")
	}
	req := make([]byte, 44)
	req[0] = opSendEvent
	req[1] = 0 // propagate = false
	binary.LittleEndian.PutUint16(req[2:], 11)
	binary.LittleEndian.PutUint32(req[4:], dest)
	binary.LittleEndian.PutUint32(req[8:], mask)
	copy(req[12:], ev)
	_, err := x.send(req)
	return err
}

// clientMessage32 builds a format-32 ClientMessage.
func clientMessage32(window, msgType uint32, data [5]uint32) []byte {
	ev := make([]byte, 32)
	ev[0] = evClientMessage
	ev[1] = 32 // format
	binary.LittleEndian.PutUint32(ev[4:], window)
	binary.LittleEndian.PutUint32(ev[8:], msgType)
	for i, v := range data {
		binary.LittleEndian.PutUint32(ev[12+4*i:], v)
	}
	return ev
}

// clientMessageData reads the five 32-bit words out of a ClientMessage event.
func clientMessageData(p xpacket) (msgType uint32, data [5]uint32, ok bool) {
	if p.kind != evClientMessage || len(p.data) < 32 || p.data[1] != 32 {
		return 0, data, false
	}
	msgType = binary.LittleEndian.Uint32(p.data[8:12])
	for i := range data {
		data[i] = binary.LittleEndian.Uint32(p.data[12+4*i:])
	}
	return msgType, data, true
}

// nextEvent returns the next queued event, or reads one from the wire.
func (x *xconn) nextEvent() (xpacket, error) {
	for {
		if len(x.events) > 0 {
			p := x.events[0]
			x.events = x.events[1:]
			return p, nil
		}
		p, err := x.readPacket()
		if err != nil {
			return xpacket{}, err
		}
		if p.kind >= 2 {
			return p, nil
		}
		// A reply or error with nothing waiting for it: a no-reply request
		// failed. Nothing to do but keep reading for the echo.
	}
}

// setDeadline bounds every subsequent read and write on the connection.
func (x *xconn) setDeadline(t time.Time) { _ = x.c.SetDeadline(t) }
