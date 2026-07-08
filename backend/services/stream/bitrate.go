package stream

import (
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Quality levels for adaptive bitrate.
// Each level defines video bitrate in kbps.
type Quality int

const (
	QualityLow       Quality = 0 // 500 kbps — high loss / slow connection
	QualityMedium    Quality = 1 // 1500 kbps — default
	QualityHigh      Quality = 2 // 2500 kbps — good connection
	QualityMax       Quality = 3 // 4000 kbps — excellent connection
	QualityGaming    Quality = 4 // 6000 kbps — gaming default
	QualityGamingMax Quality = 5 // 10000 kbps — gaming, excellent connection
)

var qualityBitrates = map[Quality]int{
	QualityLow:       500,
	QualityMedium:    1500,
	QualityHigh:      2500,
	QualityMax:       4000,
	QualityGaming:    6000,
	QualityGamingMax: 10000,
}

func (q Quality) Bitrate() int {
	if b, ok := qualityBitrates[q]; ok {
		return b
	}
	return 1500
}

func (q Quality) String() string {
	switch q {
	case QualityLow:
		return "low"
	case QualityMedium:
		return "medium"
	case QualityHigh:
		return "high"
	case QualityMax:
		return "max"
	case QualityGaming:
		return "gaming"
	case QualityGamingMax:
		return "gaming-max"
	default:
		return "medium"
	}
}

// resolutionStep holds a resolution tier used by the resolution-adaptive ABR.
type resolutionStep struct {
	width, height int
}

// resolutionLadder is the ordered resolution-step ladder (highest first).
// STREAMWIN-04: on sustained loss the controller walks down; on recovery it walks up.
var resolutionLadder = []resolutionStep{
	{1920, 1080},
	{1280, 720},
	{854, 480},
}

// bitrateController monitors a WebRTC peer connection and signals when
// the stream quality should change. It reads RTCP stats every few seconds.
//
// STREAMWIN-04: when gaming=false, sustained loss/RTT also steps down
// the resolution via resizeFn; hysteresis prevents oscillation.
type bitrateController struct {
	mu        sync.Mutex
	pc        *webrtc.PeerConnection
	current   Quality
	onChange  func(Quality)
	stop      chan struct{}
	closeOnce sync.Once

	// STREAMWIN-04: resolution adaptation (non-gaming only).
	gaming   bool
	resizeFn func(w, h int) // calls Session.Resize; nil = disabled
	// resIdx is the current index into resolutionLadder (0 = highest).
	resIdx int
	// downCount / upCount are hysteresis counters:
	// resolution steps down after downThreshold consecutive bad evaluations,
	// and steps up after upThreshold consecutive good evaluations.
	downCount int
	upCount   int
}

const resDownThreshold = 3 // consecutive bad evals before stepping resolution down
const resUpThreshold = 5   // consecutive good evals before stepping resolution up

// newBitrateController creates a controller with optional resolution adaptation.
// Pass resizeFn=nil or gaming=true to disable resolution stepping.
func newBitrateController(pc *webrtc.PeerConnection, initial Quality, onChange func(Quality)) *bitrateController {
	return newBitrateControllerFull(pc, initial, onChange, false, nil)
}

// newBitrateControllerFull creates a controller with explicit gaming flag and resize hook.
func newBitrateControllerFull(pc *webrtc.PeerConnection, initial Quality, onChange func(Quality), gaming bool, resizeFn func(w, h int)) *bitrateController {
	bc := &bitrateController{
		pc:       pc,
		current:  initial,
		onChange: onChange,
		stop:     make(chan struct{}),
		gaming:   gaming,
		resizeFn: resizeFn,
	}
	go bc.run()
	return bc
}

// Close stops the controller's run loop. It is idempotent and
// concurrency-safe: a double or concurrent Close is a no-op, not a panic.
// (The previous select-on-channel form could race two goroutines into
// close(bc.stop) simultaneously, panicking with "close of closed channel".)
func (bc *bitrateController) Close() {
	bc.closeOnce.Do(func() {
		close(bc.stop)
	})
}

func (bc *bitrateController) run() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Give the connection a few seconds to stabilize
	select {
	case <-time.After(10 * time.Second):
	case <-bc.stop:
		return
	}

	for {
		select {
		case <-ticker.C:
			bc.evaluate()
		case <-bc.stop:
			return
		}
	}
}

func (bc *bitrateController) evaluate() {
	stats := bc.pc.GetStats()

	var totalPacketsSent uint32
	var totalPacketsLost uint32
	var rtt float64
	var rttCount int

	for _, s := range stats {
		// Look for outbound RTP stats (video)
		if outbound, ok := s.(webrtc.OutboundRTPStreamStats); ok {
			if outbound.Kind == "video" {
				totalPacketsSent += outbound.PacketsSent
			}
		}
		// Look for remote inbound stats (RTCP receiver reports)
		if remote, ok := s.(webrtc.RemoteInboundRTPStreamStats); ok {
			totalPacketsLost += uint32(remote.PacketsLost)
			if remote.RoundTripTime > 0 {
				rtt += remote.RoundTripTime
				rttCount++
			}
		}
	}

	var avgRTT float64
	if rttCount > 0 {
		avgRTT = rtt / float64(rttCount)
	}

	var lossRate float64
	if totalPacketsSent > 0 {
		lossRate = float64(totalPacketsLost) / float64(totalPacketsSent) * 100
	}

	bc.mu.Lock()
	prev := bc.current

	// Decide new quality level based on loss rate and RTT
	var next Quality
	switch {
	case lossRate > 5 || avgRTT > 0.3:
		next = QualityLow
	case lossRate > 2 || avgRTT > 0.15:
		next = QualityMedium
	case lossRate < 0.5 && avgRTT < 0.05:
		next = QualityMax
	case lossRate < 1 && avgRTT < 0.1:
		next = QualityHigh
	default:
		next = QualityMedium
	}

	// Only allow one step change at a time to avoid oscillation
	if next > prev+1 {
		next = prev + 1
	}
	if next < prev-1 {
		next = prev - 1
	}

	bc.current = next

	bc.mu.Unlock()

	if next != prev {
		log.Printf("[stream] adaptive bitrate: %s → %s (loss=%.1f%%, rtt=%.0fms)",
			prev, next, lossRate, avgRTT*1000)
		if bc.onChange != nil {
			bc.onChange(next)
		}
	}

	// STREAMWIN-04: resolution adaptation alongside bitrate (non-gaming only).
	bc.applyResolutionAdaptation(lossRate, avgRTT)
}

// applyResolutionAdaptation updates the resolution hysteresis counters and,
// when a threshold is crossed, calls resizeFn with the new resolution.
// It is a no-op when gaming=true or resizeFn=nil.
// Extracted as a separate method so it can be called directly in tests without
// needing a live WebRTC peer connection.
func (bc *bitrateController) applyResolutionAdaptation(lossRate, avgRTT float64) {
	if bc.gaming || bc.resizeFn == nil {
		return
	}

	linkBad := lossRate > 2 || avgRTT > 0.15
	linkGood := lossRate < 0.5 && avgRTT < 0.08

	bc.mu.Lock()
	var resStep *resolutionStep
	if linkBad {
		bc.upCount = 0
		bc.downCount++
		if bc.downCount >= resDownThreshold && bc.resIdx < len(resolutionLadder)-1 {
			bc.resIdx++
			bc.downCount = 0
			step := resolutionLadder[bc.resIdx]
			resStep = &step
		}
	} else if linkGood {
		bc.downCount = 0
		bc.upCount++
		if bc.upCount >= resUpThreshold && bc.resIdx > 0 {
			bc.resIdx--
			bc.upCount = 0
			step := resolutionLadder[bc.resIdx]
			resStep = &step
		}
	} else {
		// Neutral: decay both counters toward zero to avoid stale hysteresis.
		if bc.downCount > 0 {
			bc.downCount--
		}
		if bc.upCount > 0 {
			bc.upCount--
		}
	}
	bc.mu.Unlock()

	if resStep != nil {
		log.Printf("[stream] adaptive resolution: → %dx%d (loss=%.1f%%, rtt=%.0fms)",
			resStep.width, resStep.height, lossRate, avgRTT*1000)
		bc.resizeFn(resStep.width, resStep.height)
	}
}
