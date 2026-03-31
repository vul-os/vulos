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
	QualityLow    Quality = 0 // 500 kbps — high loss / slow connection
	QualityMedium Quality = 1 // 1500 kbps — default
	QualityHigh   Quality = 2 // 2500 kbps — good connection
	QualityMax    Quality = 3 // 4000 kbps — excellent connection
)

var qualityBitrates = map[Quality]int{
	QualityLow:    500,
	QualityMedium: 1500,
	QualityHigh:   2500,
	QualityMax:    4000,
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
	default:
		return "medium"
	}
}

// bitrateController monitors a WebRTC peer connection and signals when
// the stream quality should change. It reads RTCP stats every few seconds.
type bitrateController struct {
	mu      sync.Mutex
	pc      *webrtc.PeerConnection
	current Quality
	onChange func(Quality)
	stop    chan struct{}
}

func newBitrateController(pc *webrtc.PeerConnection, initial Quality, onChange func(Quality)) *bitrateController {
	bc := &bitrateController{
		pc:       pc,
		current:  initial,
		onChange: onChange,
		stop:     make(chan struct{}),
	}
	go bc.run()
	return bc
}

func (bc *bitrateController) Close() {
	select {
	case <-bc.stop:
	default:
		close(bc.stop)
	}
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
}
