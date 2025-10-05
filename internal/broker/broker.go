package broker

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Bay0312/cafelog/internal/proto"
)

// ===========================
// Metrics (registered once)
// ===========================

var (
	metricsOnce sync.Once

	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "cafelog",
			Name:      "requests_total",
			Help:      "Total protocol requests by type",
		},
		[]string{"type"},
	)
	errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "cafelog",
			Name:      "errors_total",
			Help:      "Total broker logical errors by code",
		},
		[]string{"code"},
	)
	connectionsGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "cafelog",
			Name:      "tcp_connections_current",
			Help:      "Current number of active TCP connections",
		},
	)
)

func registerMetrics() {
	metricsOnce.Do(func() {
		prometheus.MustRegister(requestsTotal, errorsTotal, connectionsGauge)
	})
}

func labelForType(t uint8) string {
	switch t {
	case proto.TypeCreateTopic:
		return "CREATE_TOPIC"
	case proto.TypeProduce:
		return "PRODUCE"
	case proto.TypeFetch:
		return "FETCH"
	case proto.TypeCommit:
		return "COMMIT"
	case proto.TypeHeartbeat:
		return "HEARTBEAT"
	default:
		return "UNKNOWN"
	}
}

// ===========================
// Logical error codes (spec)
// ===========================

const (
	ErrInvalidRequest          = "INVALID_REQUEST"
	ErrUnknownTopicOrPartition = "UNKNOWN_TOPIC_OR_PARTITION"
	ErrOffsetOutOfRange        = "OFFSET_OUT_OF_RANGE"
	ErrInternalIO              = "INTERNAL_IO_ERROR"
)

// ===========================
// In-memory data structures
// ===========================

type Record struct {
	Offset int64  `json:"offset"`
	Key    []byte `json:"-"`
	Value  []byte `json:"-"`
	Ts     int64  `json:"ts"`
}

type Partition struct {
	mu      sync.RWMutex
	records []Record
}

func (p *Partition) appendRecs(in []Record) (base int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	base = int64(len(p.records))
	for i := range in {
		in[i].Offset = base + int64(i)
	}
	p.records = append(p.records, in...)
	return base
}

func (p *Partition) fetch(start int64, max int) (out []Record, highWatermark int64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	hw := int64(len(p.records))

	// Semantics: -1 means latest (return empty, next offset == hw)
	if start < 0 {
		start = hw
	}
	if start > hw {
		start = hw
	}
	if max <= 0 {
		max = len(p.records)
	}
	end := start + int64(max)
	if end > hw {
		end = hw
	}
	cp := make([]Record, end-start)
	copy(cp, p.records[start:end])
	return cp, hw
}

type Topic struct {
	Partitions []*Partition
}

type Broker struct {
	mu      sync.RWMutex
	topics  map[string]*Topic
	offsets map[string]map[string]map[int]int64 // group -> topic -> partition -> nextOffset
}

// ===========================
// Public constructor
// ===========================

func New() *Broker {
	registerMetrics()
	return &Broker{
		topics:  make(map[string]*Topic),
		offsets: make(map[string]map[string]map[int]int64),
	}
}

// ===========================
// Public TCP server
// ===========================

func (b *Broker) ServeTCP(l net.Listener) error {
	log.Printf("TCP broker listening on %s", l.Addr())
	for {
		conn, err := l.Accept()
		if err != nil {
			// If listener is closed during shutdown, just return
			if ne, ok := err.(net.Error); ok && !ne.Temporary() {
				return err
			}
			return err
		}
		connectionsGauge.Inc()
		go func(c net.Conn) {
			defer func() {
				connectionsGauge.Dec()
				_ = c.Close()
			}()
			b.serveConn(c)
		}(conn)
	}
}

// ===========================
// Connection loop
// ===========================

func (b *Broker) serveConn(c net.Conn) {
	_ = c.SetDeadline(time.Time{}) // no deadline for now
	for {
		frame, err := proto.ReadFrame(c)
		if err != nil {
			// Normal connection close
			if errors.Is(err, io.EOF) || isUseOfClosed(err) {
				return
			}
			// Protocol/read error -> emit INVALID_REQUEST and close
			writeError(c, frame.Type, ErrInvalidRequest, err)
			return
		}

		requestsTotal.WithLabelValues(labelForType(frame.Type)).Inc()

		switch frame.Type {
		case proto.TypeCreateTopic:
			var req CreateTopicRequest
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				writeError(c, frame.Type, ErrInvalidRequest, err)
				continue
			}
			resp, berr := b.handleCreateTopic(req)
			if berr != nil {
				writeError(c, frame.Type, berr.Error, errors.New(berr.Error))
				continue
			}
			writeJSON(c, frame.Type, resp)

		case proto.TypeProduce:
			var req ProduceRequest
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				writeError(c, frame.Type, ErrInvalidRequest, err)
				continue
			}
			resp, berr := b.handleProduce(req)
			if berr != nil {
				writeError(c, frame.Type, berr.Error, errors.New(berr.Error))
				continue
			}
			writeJSON(c, frame.Type, resp)

		case proto.TypeFetch:
			var req FetchRequest
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				writeError(c, frame.Type, ErrInvalidRequest, err)
				continue
			}
			resp, berr := b.handleFetch(req)
			if berr != nil {
				writeError(c, frame.Type, berr.Error, errors.New(berr.Error))
				continue
			}
			writeJSON(c, frame.Type, resp)

		case proto.TypeCommit:
			var req CommitRequest
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				writeError(c, frame.Type, ErrInvalidRequest, err)
				continue
			}
			resp, berr := b.handleCommit(req)
			if berr != nil {
				writeError(c, frame.Type, berr.Error, errors.New(berr.Error))
				continue
			}
			writeJSON(c, frame.Type, resp)

		case proto.TypeHeartbeat:
			// Simple echo OK (keeps the connection alive)
			writeJSON(c, frame.Type, map[string]any{"ok": true})

		default:
			writeError(c, frame.Type, ErrInvalidRequest, fmt.Errorf("unknown type %d", frame.Type))
		}
	}
}

func isUseOfClosed(err error) bool {
	// Best-effort detect closed conn without importing internal net errors
	if err == nil {
		return false
	}
	es := err.Error()
	return es == "use of closed network connection"
}

// ===========================
// Requests / Responses
// ===========================

type CreateTopicRequest struct {
	Topic      string `json:"topic"`
	Partitions int    `json:"partitions"`
}
type CreateTopicResponse struct {
	OK bool `json:"ok"`
}

type RecordJSON struct {
	Key   string `json:"key"`   // base64
	Value string `json:"value"` // base64
	Ts    int64  `json:"ts"`
}

type ProduceRequest struct {
	Topic     string       `json:"topic"`
	Partition int          `json:"partition"` // -1 => route by key hash
	Records   []RecordJSON `json:"records"`
}
type ProduceResponse struct {
	Topic         string `json:"topic"`
	Partition     int    `json:"partition"`
	BaseOffset    int64  `json:"baseOffset"`
	NumRecords    int    `json:"numRecords"`
	HighWatermark int64  `json:"highWatermark"`
}

type FetchRequest struct {
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Offset    int64  `json:"offset"`    // -1 latest, 0 from-beginning
	MaxBytes  int    `json:"maxBytes"`  // ignored in H0
	MaxWaitMs int    `json:"maxWaitMs"` // ignored (no long-poll yet)
}
type FetchedRecord struct {
	Offset int64  `json:"offset"`
	Key    string `json:"key"`   // base64
	Value  string `json:"value"` // base64
	Ts     int64  `json:"ts"`
}
type FetchResponse struct {
	Records       []FetchedRecord `json:"records"`
	HighWatermark int64           `json:"highWatermark"`
}

type CommitRequest struct {
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Group     string `json:"group"`
	Offset    int64  `json:"offset"` // next to read
}
type CommitResponse struct {
	OK bool `json:"ok"`
}

type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ===========================
// Handlers (in-memory)
// ===========================

type brokerErr struct{ Error string }

func (b *Broker) handleCreateTopic(req CreateTopicRequest) (*CreateTopicResponse, *brokerErr) {
	if req.Topic == "" || req.Partitions <= 0 {
		return nil, &brokerErr{ErrInvalidRequest}
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.topics[req.Topic]; ok {
		// Idempotent create
		return &CreateTopicResponse{OK: true}, nil
	}
	t := &Topic{Partitions: make([]*Partition, req.Partitions)}
	for i := range t.Partitions {
		t.Partitions[i] = &Partition{}
	}
	b.topics[req.Topic] = t
	return &CreateTopicResponse{OK: true}, nil
}

func (b *Broker) handleProduce(req ProduceRequest) (*ProduceResponse, *brokerErr) {
	if req.Topic == "" || len(req.Records) == 0 {
		return nil, &brokerErr{ErrInvalidRequest}
	}

	topic, ok := b.getTopic(req.Topic)
	if !ok {
		return nil, &brokerErr{ErrUnknownTopicOrPartition}
	}

	part := req.Partition
	if part < 0 {
		// Route by key hash (first record key)
		if len(req.Records) > 0 {
			kb, _ := tryB64(req.Records[0].Key)
			h := sha1.Sum(kb)
			part = int(h[0]) % len(topic.Partitions)
		} else {
			part = 0
		}
	}
	if part >= len(topic.Partitions) {
		return nil, &brokerErr{ErrUnknownTopicOrPartition}
	}

	recs := make([]Record, 0, len(req.Records))
	for _, rj := range req.Records {
		kb, _ := tryB64(rj.Key)
		vb, _ := tryB64(rj.Value)
		recs = append(recs, Record{
			Key:   kb,
			Value: vb,
			Ts:    rj.Ts,
		})
	}

	base := topic.Partitions[part].appendRecs(recs)
	_, hw := topic.Partitions[part].fetch(0, 0)

	return &ProduceResponse{
		Topic:         req.Topic,
		Partition:     part,
		BaseOffset:    base,
		NumRecords:    len(recs),
		HighWatermark: hw,
	}, nil
}

func (b *Broker) handleFetch(req FetchRequest) (*FetchResponse, *brokerErr) {
	if req.Topic == "" || req.Partition < 0 {
		return nil, &brokerErr{ErrInvalidRequest}
	}
	topic, ok := b.getTopic(req.Topic)
	if !ok || req.Partition >= len(topic.Partitions) {
		return nil, &brokerErr{ErrUnknownTopicOrPartition}
	}

	recs, hw := topic.Partitions[req.Partition].fetch(req.Offset, 0)
	out := make([]FetchedRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, FetchedRecord{
			Offset: r.Offset,
			Key:    base64.StdEncoding.EncodeToString(r.Key),
			Value:  base64.StdEncoding.EncodeToString(r.Value),
			Ts:     r.Ts,
		})
	}
	return &FetchResponse{Records: out, HighWatermark: hw}, nil
}

func (b *Broker) handleCommit(req CommitRequest) (*CommitResponse, *brokerErr) {
	if req.Topic == "" || req.Partition < 0 || req.Group == "" || req.Offset < 0 {
		return nil, &brokerErr{ErrInvalidRequest}
	}
	if _, ok := b.getTopic(req.Topic); !ok {
		return nil, &brokerErr{ErrUnknownTopicOrPartition}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.offsets[req.Group]; !ok {
		b.offsets[req.Group] = make(map[string]map[int]int64)
	}
	if _, ok := b.offsets[req.Group][req.Topic]; !ok {
		b.offsets[req.Group][req.Topic] = make(map[int]int64)
	}
	// Idempotent: always set "next offset to read"
	b.offsets[req.Group][req.Topic][req.Partition] = req.Offset
	return &CommitResponse{OK: true}, nil
}

// ===========================
// Helpers
// ===========================

func (b *Broker) getTopic(name string) (*Topic, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	t, ok := b.topics[name]
	return t, ok
}

func tryB64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// If it's not valid base64, treat as raw bytes (dev-friendly)
		return []byte(s), nil
	}
	return b, nil
}

func writeJSON(conn net.Conn, typ uint8, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeError(conn, typ, ErrInternalIO, err)
		return
	}
	_ = proto.WriteFrame(conn, typ, body)
}

func writeError(conn net.Conn, typ uint8, code string, cause error) {
	errorsTotal.WithLabelValues(code).Inc()
	var er ErrorResponse
	er.Error.Code = code
	if cause != nil {
		er.Error.Message = cause.Error()
	} else {
		er.Error.Message = code
	}
	body, _ := json.Marshal(er)
	_ = proto.WriteFrame(conn, typ, body)
}