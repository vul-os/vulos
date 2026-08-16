package proctl

// A scripted X server, just complete enough to be lied to in specific ways.
//
// # Why a fake at all
//
// The four answers ProbeX11Ping can give correspond to four behaviours of the
// other end: echo, silence-with-a-live-server, silence-with-a-dead-server, and
// nothing that implements the protocol. Only the first two are easy to produce
// with a real X server and a real toolkit app; the third needs a stalled Xvfb
// and the fourth needs an app that deliberately omits _NET_WM_PING. All four
// are one field of this fake.
//
// It also runs on macOS, where `ip netns`, Xvfb and /proc do not exist, so the
// decision logic is covered on the machine the work is done on rather than only
// in a container.
//
// # What it does NOT prove
//
// That the byte layouts match a real X server, and that a real toolkit app
// answers a ping at all. A fake agreeing with the client that wrote it proves
// only that they agree with each other — this repo's recorded lesson from the
// kotva-cbor corpus. That half is proved separately against a real Xvfb, a real
// matchbox window manager and a real GTK application; see xping_container_test
// notes in the commit for this file.

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeX is a scripted X server on a unix socket.
type fakeX struct {
	// tree maps a window to its children. Window 1 is the root.
	tree map[uint32][]uint32
	// pingable is the set of windows whose WM_PROTOCOLS lists _NET_WM_PING.
	pingable map[uint32]bool
	// echo makes a pinged window answer, i.e. the application is alive.
	echo bool
	// echoDelay defers the echo, so the probe has to read it after the
	// post-send round trip rather than finding it already queued.
	echoDelay time.Duration
	// stallAfter makes the server stop answering after this many requests.
	// 0 means never stall. This is the wedged-X-server case.
	stallAfter int
	// wrongWindow makes the echo name a window that was never pinged, which is
	// what a stray or spoofed echo looks like.
	wrongWindow uint32

	socket string
	ln     net.Listener
	atoms  map[string]uint32
	mu     sync.Mutex
}

const fakeRootWindow = 1

// startFakeX brings up the server and returns its socket path.
func startFakeX(t *testing.T, f *fakeX) string {
	t.Helper()
	// A short directory on purpose: a unix socket path is capped at ~104 bytes
	// and t.TempDir() plus a long test name overruns it on macOS.
	dir, err := os.MkdirTemp("", "xp")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	f.socket = filepath.Join(dir, "X0")
	f.atoms = map[string]uint32{}
	if f.tree == nil {
		f.tree = map[uint32][]uint32{}
	}
	if f.pingable == nil {
		f.pingable = map[uint32]bool{}
	}
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatalf("listen %s: %v", f.socket, err)
	}
	f.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		os.RemoveAll(dir)
	})
	return f.socket
}

func (f *fakeX) atom(name string) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.atoms[name]; ok {
		return a
	}
	a := uint32(1000 + len(f.atoms))
	f.atoms[name] = a
	return a
}

func (f *fakeX) serve(c net.Conn) {
	defer c.Close()

	// Connection setup.
	head := make([]byte, 12)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	nameLen := int(binary.LittleEndian.Uint16(head[6:8]))
	dataLen := int(binary.LittleEndian.Uint16(head[8:10]))
	if n := pad4(nameLen) + pad4(dataLen); n > 0 {
		io.ReadFull(c, make([]byte, n))
	}

	body := make([]byte, 32+40)                       // fixed fields, no vendor, no formats, one screen
	binary.LittleEndian.PutUint32(body[4:], 0x400000) // resource-id-base
	binary.LittleEndian.PutUint32(body[8:], 0x1fffff) // resource-id-mask
	binary.LittleEndian.PutUint16(body[16:], 0)       // vendor length
	binary.LittleEndian.PutUint16(body[18:], 65535)   // max-request-length
	body[20] = 1                                      // number of screens
	body[21] = 0                                      // number of formats
	binary.LittleEndian.PutUint32(body[32:], fakeRootWindow)
	reply := make([]byte, 8)
	reply[0] = 1 // success
	binary.LittleEndian.PutUint16(reply[2:], 11)
	binary.LittleEndian.PutUint16(reply[6:], uint16(len(body)/4))
	if _, err := c.Write(append(reply, body...)); err != nil {
		return
	}

	var seq uint16
	requests := 0
	var wmu sync.Mutex
	write := func(b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		c.Write(b)
	}

	for {
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		words := int(binary.LittleEndian.Uint16(hdr[2:4]))
		if words < 1 {
			return
		}
		rest := make([]byte, (words-1)*4)
		if _, err := io.ReadFull(c, rest); err != nil {
			return
		}
		req := append(hdr, rest...)
		seq++
		requests++
		if f.stallAfter > 0 && requests > f.stallAfter {
			// The wedged server: it is still connected, the socket is still
			// open, and it never answers again.
			time.Sleep(time.Hour)
			return
		}

		switch req[0] {
		case opInternAtom:
			n := int(binary.LittleEndian.Uint16(req[4:6]))
			name := string(req[8 : 8+n])
			out := make([]byte, 32)
			out[0] = 1
			binary.LittleEndian.PutUint16(out[2:], seq)
			binary.LittleEndian.PutUint32(out[8:], f.atom(name))
			write(out)

		case opQueryTree:
			win := binary.LittleEndian.Uint32(req[4:8])
			kids := f.tree[win]
			out := make([]byte, 32+4*len(kids))
			out[0] = 1
			binary.LittleEndian.PutUint16(out[2:], seq)
			binary.LittleEndian.PutUint32(out[4:], uint32(len(kids)))
			binary.LittleEndian.PutUint32(out[8:], fakeRootWindow)
			binary.LittleEndian.PutUint16(out[16:], uint16(len(kids)))
			for i, k := range kids {
				binary.LittleEndian.PutUint32(out[32+4*i:], k)
			}
			write(out)

		case opGetProperty:
			win := binary.LittleEndian.Uint32(req[4:8])
			prop := binary.LittleEndian.Uint32(req[8:12])
			out := make([]byte, 32)
			out[0] = 1
			binary.LittleEndian.PutUint16(out[2:], seq)
			if prop == f.atom("WM_PROTOCOLS") && f.pingable[win] {
				out[1] = 32 // format
				binary.LittleEndian.PutUint32(out[4:], 1)
				binary.LittleEndian.PutUint32(out[8:], atomAtom)
				binary.LittleEndian.PutUint32(out[16:], 1) // one 32-bit unit
				val := make([]byte, 4)
				binary.LittleEndian.PutUint32(val, f.atom("_NET_WM_PING"))
				out = append(out, val...)
			}
			write(out)

		case opGetInputFocus:
			out := make([]byte, 32)
			out[0] = 1
			binary.LittleEndian.PutUint16(out[2:], seq)
			write(out)

		case opChangeWindowAttributes:
			// No reply, by protocol.

		case opSendEvent:
			if !f.echo {
				continue
			}
			ev := req[12:44]
			target := binary.LittleEndian.Uint32(ev[20:24]) // data[2]
			if f.wrongWindow != 0 {
				target = f.wrongWindow
			}
			// What a real toolkit sends back: the same message, addressed to
			// the root window, with the sent-by-a-client bit set.
			out := make([]byte, 32)
			copy(out, ev)
			out[0] = evClientMessage | sentBit
			binary.LittleEndian.PutUint32(out[4:], fakeRootWindow)
			binary.LittleEndian.PutUint32(out[20:], target)
			if f.echoDelay > 0 {
				go func() { time.Sleep(f.echoDelay); write(out) }()
			} else {
				write(out)
			}
		}
	}
}
