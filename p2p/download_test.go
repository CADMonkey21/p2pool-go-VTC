package p2p

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type fakeFetch struct {
	pid  peer.ID
	req  ShareReq
	got  int
	err  error
	reqs int
}

func (f *fakeFetch) fetch(pid peer.ID, req ShareReq) (int, error) {
	f.pid, f.req = pid, req
	f.reqs++
	return f.got, f.err
}

func hashN(i int) string { return fmt.Sprintf("%064x", i) }

func newTestDownloader(missing, stops []string, peers []peer.ID, f *fakeFetch) (*shareDownloader, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	d := &shareDownloader{
		missing:  func() []string { return missing },
		stops:    func() []string { return stops },
		peers:    func() []peer.ID { return peers },
		fetch:    f.fetch,
		intn:     func(n int) int { return n - 1 },
		now:      func() time.Time { return now },
		answered: make(map[string]time.Time),
	}
	return d, &now
}

func TestDownloadStepRequest(t *testing.T) {
	stops := make([]string, 150)
	for i := range stops {
		stops[i] = hashN(1000 + i)
	}
	f := &fakeFetch{got: 300}
	d, _ := newTestDownloader([]string{hashN(1), hashN(2)}, stops, []peer.ID{"a", "b"}, f)

	if wait := d.step(); wait != 0 {
		t.Fatalf("reply with shares: wait %v, want 0", wait)
	}
	if f.pid != "b" {
		t.Fatalf("asked peer %q, want the randomly chosen b", f.pid)
	}
	if len(f.req.Hashes) != 1 || f.req.Hashes[0] != hashN(2) {
		t.Fatalf("requested %v, want the randomly chosen missing hash alone", f.req.Hashes)
	}
	if f.req.Parents != maxDownloadParents-1 {
		t.Fatalf("parents %d, want a random value below %d", f.req.Parents, maxDownloadParents)
	}
	if len(f.req.Stops) != maxStopHashes || f.req.Stops[0] != stops[0] {
		t.Fatalf("sent %d stops, want the first %d", len(f.req.Stops), maxStopHashes)
	}
}

func TestDownloadStepNothingToDo(t *testing.T) {
	f := &fakeFetch{got: 1}
	d, _ := newTestDownloader(nil, nil, []peer.ID{"a"}, f)
	if wait := d.step(); wait != downloadRetryDelay || f.reqs != 0 {
		t.Fatalf("no missing parents: wait %v after %d requests, want %v after none", wait, f.reqs, downloadRetryDelay)
	}

	d, _ = newTestDownloader([]string{hashN(1)}, nil, nil, f)
	if wait := d.step(); wait != downloadRetryDelay || f.reqs != 0 {
		t.Fatalf("no peers: wait %v after %d requests, want %v after none", wait, f.reqs, downloadRetryDelay)
	}
}

func TestDownloadStepEmptyOrFailedReply(t *testing.T) {
	f := &fakeFetch{got: 0}
	d, _ := newTestDownloader([]string{hashN(1)}, nil, []peer.ID{"a"}, f)
	if wait := d.step(); wait != downloadRetryDelay {
		t.Fatalf("empty reply: wait %v, want %v", wait, downloadRetryDelay)
	}
	// Nobody had it, so it is asked for again on the next pass.
	if d.step(); f.reqs != 2 {
		t.Fatalf("after empty reply: %d requests, want 2", f.reqs)
	}

	f.err = errors.New("stream reset")
	if wait := d.step(); wait != downloadRetryDelay || f.reqs != 3 {
		t.Fatalf("failed request: wait %v after %d requests, want %v after 3", wait, f.reqs, downloadRetryDelay)
	}
}

func TestDownloadStepSkipsRejectedHash(t *testing.T) {
	f := &fakeFetch{got: 10}
	d, now := newTestDownloader([]string{hashN(1)}, nil, []peer.ID{"a"}, f)
	d.step()

	// The hash is still missing after a reply that held it, so it is held back.
	if wait := d.step(); wait != downloadRetryDelay || f.reqs != 1 {
		t.Fatalf("rejected hash: wait %v after %d requests, want %v after 1", wait, f.reqs, downloadRetryDelay)
	}

	*now = now.Add(downloadRequeryDelay)
	if d.step(); f.reqs != 2 {
		t.Fatalf("after %v: %d requests, want 2", downloadRequeryDelay, f.reqs)
	}
}
