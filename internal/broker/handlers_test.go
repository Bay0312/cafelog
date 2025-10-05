package broker

import (
	"encoding/base64"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestCreateProduceFetchCommitHappyPath(t *testing.T) {
	br := New()

	// Create topic with 2 partitions
	if _, be := br.handleCreateTopic(CreateTopicRequest{Topic: "user-events", Partitions: 2}); be != nil {
		t.Fatalf("create topic: %v", be)
	}

	// Produce two records using partition = -1 (hash by key)
	presp, be := br.handleProduce(ProduceRequest{
		Topic:     "user-events",
		Partition: -1,
		Records: []RecordJSON{
			{Key: b64("user1"), Value: b64(`{"a":1}`), Ts: 1},
			{Key: b64("user2"), Value: b64(`{"a":2}`), Ts: 2},
		},
	})
	if be != nil {
		t.Fatalf("produce: %v", be)
	}
	if presp.NumRecords != 2 {
		t.Fatalf("expected 2 appended, got %d", presp.NumRecords)
	}

	// Since routing is by key hash, records could land in different partitions.
	// Fetch both partitions from 0 and sum up.
	var total int
	for p := 0; p < 2; p++ {
		fresp, be := br.handleFetch(FetchRequest{
			Topic:     "user-events",
			Partition: p,
			Offset:    0,
		})
		if be != nil {
			t.Fatalf("fetch p=%d: %v", p, be)
		}
		total += len(fresp.Records)
	}
	if total != 2 {
		t.Fatalf("expected 2 total fetched, got %d", total)
	}

	// Commit next offset for group
	cresp, be := br.handleCommit(CommitRequest{
		Topic:     "user-events",
		Partition: 0,
		Group:     "analytics",
		Offset:    10,
	})
	if be != nil || !cresp.OK {
		t.Fatalf("commit failed: %v", be)
	}
}