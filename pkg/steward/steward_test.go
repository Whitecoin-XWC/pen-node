// Copyright 2021 The Penguin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package steward_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/penguintop/penguin/pkg/file/pipeline/builder"
	"github.com/penguintop/penguin/pkg/pushsync"
	psmock "github.com/penguintop/penguin/pkg/pushsync/mock"
	"github.com/penguintop/penguin/pkg/steward"
	"github.com/penguintop/penguin/pkg/storage"
	"github.com/penguintop/penguin/pkg/storage/mock"
    "github.com/penguintop/penguin/pkg/penguin"
	"github.com/penguintop/penguin/pkg/traversal"
)

func TestSteward(t *testing.T) {
	var (
		ctx            = context.Background()
		chunks         = 1000
		data           = make([]byte, chunks*4096) //1k chunks
		store          = mock.NewStorer()
		traverser      = traversal.New(store)
		traversedAddrs = make(map[string]struct{})
		mu             sync.Mutex
		fn             = func(_ context.Context, ch penguin.Chunk) (*pushsync.Receipt, error) {
			mu.Lock()
			traversedAddrs[ch.Address().String()] = struct{}{}
			mu.Unlock()
			return nil, nil
		}
		ps = psmock.New(fn)
		s  = steward.New(store, traverser, ps)
	)
	n, err := rand.Read(data)
	if n != cap(data) {
		t.Fatal("short read")
	}
	if err != nil {
		t.Fatal(err)
	}

	l := &loggingStore{Storer: store}
	pipe := builder.NewPipelineBuilder(ctx, l, storage.ModePutUpload, false)
	addr, err := builder.FeedPipeline(ctx, pipe, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	err = s.Reupload(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()

	// check that everything that was stored is also traversed
	for _, a := range l.addrs {
		if _, ok := traversedAddrs[a.String()]; !ok {
			t.Fatalf("expected address %s to be traversed", a.String())
		}
	}
}

type loggingStore struct {
	storage.Storer
	addrs []penguin.Address
}

func (l *loggingStore) Put(ctx context.Context, mode storage.ModePut, chs ...penguin.Chunk) (exist []bool, err error) {
	for _, c := range chs {
		l.addrs = append(l.addrs, c.Address())
	}
	return l.Storer.Put(ctx, mode, chs...)
}