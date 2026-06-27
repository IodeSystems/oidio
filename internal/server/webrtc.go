package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/iodesystems/oidio/internal/engine"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
)

// handleRealtimeWebRTC serves POST /v1/realtime with an `application/sdp` offer —
// OpenAI Realtime over WebRTC. The client adds its mic as an Opus track and an
// "oai-events" data channel; we decode the track (Opus → 48 kHz PCM → the same
// streaming recognizer as the WS path) and push transcript events back over the
// channel. Non-trickle ICE: we return the gathered answer in the POST response.
func (s *Server) handleRealtimeWebRTC(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("model")
	m := s.models[name]
	if m == nil || m.rt == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("model %q has no realtime transcription", name), "invalid_request_error")
		return
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read offer: "+err.Error(), "invalid_request_error")
		return
	}

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}

	c := &rtcConn{sess: m.rt.NewSession()}
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			c.sess.Close()
			_ = pc.Close()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		c.bind(dc) // sets OnOpen → flush buffered events
		c.sendJSON(map[string]any{"type": "session.created", "session": map[string]any{"object": "realtime.transcription_session"}})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			var v struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(msg.Data, &v) != nil {
				return
			}
			switch v.Type {
			case "session.update":
				c.sendJSON(map[string]any{"type": "session.updated", "session": map[string]any{"object": "realtime.transcription_session"}})
			case "input_audio_buffer.commit":
				for _, p := range rtPayloads(c.sess.Finalize()) {
					c.sendJSON(p)
				}
			}
		})
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		dec, err := opus.NewDecoderWithOutput(48000, 1) // Opus decodes at 48 kHz, mono
		if err != nil {
			return
		}
		out := make([]float32, 5760) // 120 ms @ 48 kHz
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if len(pkt.Payload) == 0 {
				continue
			}
			n, err := dec.DecodeToFloat32(pkt.Payload, out)
			if err != nil || n == 0 {
				continue
			}
			for _, p := range rtPayloads(c.sess.AcceptRate(out[:n], 48000)) {
				c.sendJSON(p)
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(offer)}); err != nil {
		_ = pc.Close()
		writeErr(w, http.StatusBadRequest, "set remote description: "+err.Error(), "invalid_request_error")
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		writeErr(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		writeErr(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	<-gather // wait for full ICE candidates (non-trickle)

	w.Header().Set("Content-Type", "application/sdp")
	_, _ = io.WriteString(w, pc.LocalDescription().SDP)
}

// rtcConn buffers events until the data channel is open (OnTrack and the channel
// open race), then sends them in order.
type rtcConn struct {
	sess    *engine.RTSession
	mu      sync.Mutex
	dc      *webrtc.DataChannel
	open    bool
	pending []string
}

func (c *rtcConn) bind(dc *webrtc.DataChannel) {
	c.mu.Lock()
	c.dc = dc
	c.mu.Unlock()
	dc.OnOpen(func() {
		c.mu.Lock()
		c.open = true
		pend := c.pending
		c.pending = nil
		c.mu.Unlock()
		for _, p := range pend {
			_ = dc.SendText(p)
		}
	})
}

func (c *rtcConn) sendJSON(v any) {
	b, _ := json.Marshal(v)
	s := string(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open && c.dc != nil {
		_ = c.dc.SendText(s)
		return
	}
	c.pending = append(c.pending, s)
}
