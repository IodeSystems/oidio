// Package verify serves the ground-truth workbench: a local page for correcting
// who spoke each segment — and where each segment begins and ends — so
// diarization output can be SCORED rather than argued about.
//
// It exists because two signals disagreed and there was no way to tell which was
// wrong. The voice pass proposed 36 corrections and the content reviewer rejected
// 32 of them; the content reviewer proposed 79 and the voice pass contradicted
// every one that could be measured. Both stories fit the data. Only labels settle
// it.
//
// It is an EDITOR, not a survey. Attribution alone cannot express the common
// case: a diarization boundary that falls mid-sentence, leaving one speaker's
// last word attached to the next speaker's turn. Fixing that needs the segment
// list itself to change, so the workbench owns segments, not just their labels.
//
// Nothing here calls a model. It plays audio, records what a person heard, and
// writes it down.
package verify

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Segment is one corrected turn. The shape matches the diarize response so the
// output can be scored against the input field for field.
type Segment struct {
	ID      int     `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`

	// Speaker is the PERSON, as decided by a human. Mutable.
	Speaker string `json:"speaker"`

	// Cluster is what the MACHINE grouped this turn into. Immutable, and kept
	// separate from Speaker because they answer different questions and were
	// previously the same field — which is what made the tool ambiguous. Picking
	// a speaker for one turn and naming two clusters the same person both wrote
	// `speaker`, so a local correction and a global identity claim were
	// indistinguishable in the data and surprising in the UI.
	//
	// Holding the machine's grouping also makes a bulk correction possible:
	// "every turn the diarizer called this cluster is actually X" is the common
	// repair, and without the original grouping it cannot be expressed once the
	// first turn has been reassigned.
	Cluster string `json:"cluster,omitempty"`

	// Confirmed marks a turn a person has actually ruled on. Without it an
	// untouched segment and a segment judged correct are indistinguishable, and
	// a half-finished pass would score as if it were complete.
	Confirmed bool `json:"confirmed,omitempty"`

	// Unclear is a real answer: audible, but not attributable. Distinct from
	// unconfirmed, and it marks the turns where NEITHER signal should be
	// expected to be right.
	Unclear bool `json:"unclear,omitempty"`

	// Corrected marks text a person actually retyped, as opposed to text the
	// recogniser produced and nobody disputed. Only these are ground truth for
	// word error rate — scoring WER against un-reviewed output would measure the
	// recogniser against itself and report a flawless zero.
	Corrected bool `json:"corrected,omitempty"`
}

// Word is one recogniser-timed word. Times are absolute and independent of the
// segmentation, which is what makes them usable after editing.
type Word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
}

// TruthFile is what the workbench writes: the corrected transcript plus the
// labels needed to read it.
type TruthFile struct {
	Source   string            `json:"source"`
	Updated  string            `json:"updated"`
	Segments []Segment         `json:"segments"`
	Speakers map[string]string `json:"speakers,omitempty"`
}

type Server struct {
	mu sync.Mutex

	audioPath string
	transPath string
	speakPath string
	truthPath string

	original []Segment
	segments []Segment
	speakers map[string]string

	// peaks is the amplitude envelope for the waveform strip, computed once.
	peaks []byte

	// words carry ABSOLUTE times from the recogniser, so they stay correct
	// through any amount of splitting and rejoining — unlike a position within a
	// segment, which stops meaning anything the moment the segment changes.
	words []Word
}

func New(audioPath, transPath, speakPath, truthPath string) (*Server, error) {
	s := &Server{
		audioPath: audioPath, transPath: transPath,
		speakPath: speakPath, truthPath: truthPath,
		speakers: map[string]string{},
	}
	b, err := os.ReadFile(transPath)
	if err != nil {
		return nil, fmt.Errorf("transcription: %w", err)
	}
	var doc struct {
		Segments []Segment `json:"segments"`
		Words    []Word    `json:"words"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("transcription %s: %w", transPath, err)
	}
	if len(doc.Segments) == 0 {
		return nil, fmt.Errorf("%s has no segments — is it a diarize result?", transPath)
	}
	s.original = doc.Segments
	s.words = doc.Words
	s.segments = append([]Segment(nil), doc.Segments...)
	if _, err := os.Stat(audioPath); err != nil {
		return nil, fmt.Errorf("audio: %w", err)
	}
	s.loadSpeakers()
	s.loadTruth()
	s.backfillClusters()
	return s, nil
}

// loadSpeakers accepts either a map of uuid->label or the list form the diarize
// response uses, so an existing catalog can be pointed at without conversion.
func (s *Server) loadSpeakers() {
	if s.speakPath == "" {
		return
	}
	b, err := os.ReadFile(s.speakPath)
	if err != nil {
		return
	}
	var asMap map[string]string
	if json.Unmarshal(b, &asMap) == nil && len(asMap) > 0 {
		s.speakers = asMap
		return
	}
	var asList []struct {
		UUID  string `json:"uuid"`
		Label string `json:"label"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(b, &asList) == nil {
		for _, e := range asList {
			l := e.Label
			if l == "" {
				l = e.Name
			}
			if e.UUID != "" && l != "" {
				s.speakers[e.UUID] = l
			}
		}
	}
}

// loadTruth resumes an earlier pass. The saved segments REPLACE the originals,
// because a join or split means the original list no longer describes the file.
func (s *Server) loadTruth() {
	b, err := os.ReadFile(s.truthPath)
	if err != nil {
		return
	}
	var tf TruthFile
	if json.Unmarshal(b, &tf) != nil || len(tf.Segments) == 0 {
		return
	}
	s.segments = tf.Segments
	for k, v := range tf.Speakers {
		if _, ok := s.speakers[k]; !ok {
			s.speakers[k] = v
		}
	}
}

// backfillClusters recovers the machine's grouping for truth files written
// before Cluster existed, by asking the ORIGINAL transcription who it thought
// was speaking at each turn's midpoint. Reading it back from the current speaker
// would be wrong: by the time a pass is under way, that field holds human
// corrections, and the machine's grouping would be silently overwritten with the
// answers derived from it.
func (s *Server) backfillClusters() {
	for i := range s.segments {
		if s.segments[i].Cluster != "" {
			continue
		}
		mid := (s.segments[i].Start + s.segments[i].End) / 2
		for _, o := range s.original {
			if o.Start <= mid && mid < o.End {
				s.segments[i].Cluster = o.Speaker
				break
			}
		}
		if s.segments[i].Cluster == "" {
			s.segments[i].Cluster = s.segments[i].Speaker
		}
	}
}

// Listen binds a port chosen by the OS, so several hearings can be labelled at
// once without coordinating port numbers. The caller prints what it returns.
func Listen(host string) (net.Listener, string, error) { return ListenOn(host, "") }

// ListenOn binds a specific port when one is given. A random port is right for
// starting fresh, but wrong for restarting: an open page keeps posting to the
// port it loaded from, so a restart on a NEW port silently breaks every save
// while the tab still looks fine. Rebinding the same port is how in-progress
// work survives a server restart.
func ListenOn(host, port string) (net.Listener, string, error) {
	if port == "" {
		port = "0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, "", err
	}
	_, bound, _ := net.SplitHostPort(ln.Addr().String())
	return ln, fmt.Sprintf("http://%s:%s", host, bound), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	// ServeFile, not a hand-rolled copy: the page seeks constantly and needs
	// Range support, which ServeFile implements and a naive io.Copy does not.
	mux.HandleFunc("GET /audio", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, s.audioPath)
	})
	// Served separately from /api/data: it is a few hundred KB and the page is
	// usable without it, so the transcript must not wait on it.
	mux.HandleFunc("GET /api/peaks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.peaks == nil {
			p, err := Peaks(s.audioPath)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			s.peaks = p
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(s.peaks)
	})
	mux.HandleFunc("GET /api/data", s.handleData)
	mux.HandleFunc("POST /api/segments", s.handleSegments)
	mux.HandleFunc("POST /api/speaker", s.handleSpeaker)
	return mux
}

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{
		"audio":     filepath.Base(s.audioPath),
		"segments":  s.segments,
		"original":  s.original,
		"speakers":  s.speakers,
		"words":     s.words,
		"peakRate":  peaksPerSecond,
		"truthPath": s.truthPath,
	})
}

// handleSegments takes the WHOLE corrected list on every change.
//
// Sending the full list rather than an operation keeps undo entirely in the
// page — the client replays its own history and posts the result — and removes
// any chance of the two sides disagreeing about what the list currently is,
// which is the bug that would silently corrupt a labelling pass.
func (s *Server) handleSegments(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Segments []Segment `json:"segments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(in.Segments) == 0 {
		http.Error(w, "refusing to save an empty segment list", 400)
		return
	}
	s.mu.Lock()
	s.segments = in.Segments
	err := s.saveTruthLocked()
	done := 0
	for _, sg := range s.segments {
		if sg.Confirmed || sg.Unclear {
			done++
		}
	}
	n := len(s.segments)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "confirmed": done, "total": n})
}

func (s *Server) handleSpeaker(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UUID   string `json:"uuid"`
		Label  string `json:"label"`
		Remove bool   `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	// Removal is how an alias disappears: naming two ids the same person merges
	// them, and the absorbed id must leave the catalog or it lingers as a
	// speaker with no speech, offered forever in the roster.
	if in.Remove {
		delete(s.speakers, in.UUID)
	} else {
		s.speakers[in.UUID] = in.Label
	}
	err := s.saveSpeakersLocked()
	if err == nil {
		err = s.saveTruthLocked()
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// saveTruthLocked writes on every change rather than on a Save button. Labelling
// is long and dull; losing an hour of it to a closed tab is the failure that
// stops it being done at all.
func (s *Server) saveTruthLocked() error {
	tf := TruthFile{
		Source:   filepath.Base(s.transPath),
		Updated:  time.Now().Format(time.RFC3339),
		Segments: s.segments,
		Speakers: s.speakers,
	}
	b, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.truthPath, b)
}

func (s *Server) saveSpeakersLocked() error {
	if s.speakPath == "" {
		return nil
	}
	keys := make([]string, 0, len(s.speakers))
	for k := range s.speakers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = s.speakers[k]
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.speakPath, b)
}

// atomicWrite renames a complete temp file into place, so an interrupted save
// cannot truncate hours of labelling into a half-written file.
func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
