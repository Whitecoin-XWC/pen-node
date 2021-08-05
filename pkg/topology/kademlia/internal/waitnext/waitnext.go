// Copyright 2021 The Penguin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package metrics provides service for collecting various metrics about peers.
// It is intended to be used with the kademlia where the metrics are collected.
package waitnext

import (
	"sync"
	"time"

    "github.com/penguintop/penguin/pkg/penguin"
)

type next struct {
	tryAfter       time.Time
	failedAttempts int
}

type WaitNext struct {
	next map[string]*next
	sync.Mutex
}

func New() *WaitNext {
	return &WaitNext{
		next: make(map[string]*next),
	}
}

func (r *WaitNext) Set(addr penguin.Address, tryAfter time.Time, attempts int) {

	r.Lock()
	defer r.Unlock()

	r.next[addr.ByteString()] = &next{tryAfter: tryAfter, failedAttempts: attempts}
}

func (r *WaitNext) SetTryAfter(addr penguin.Address, tryAfter time.Time) {

	r.Lock()
	defer r.Unlock()

	if info, ok := r.next[addr.ByteString()]; ok {
		info.tryAfter = tryAfter
	} else {
		r.next[addr.ByteString()] = &next{tryAfter: tryAfter}
	}
}

func (r *WaitNext) Waiting(addr penguin.Address) bool {

	r.Lock()
	defer r.Unlock()

	info, ok := r.next[addr.ByteString()]
	return ok && time.Now().Before(info.tryAfter)
}

func (r *WaitNext) Attempts(addr penguin.Address) int {

	r.Lock()
	defer r.Unlock()

	if info, ok := r.next[addr.ByteString()]; ok {
		return info.failedAttempts
	}

	return 0
}

func (r *WaitNext) Remove(addr penguin.Address) {

	r.Lock()
	defer r.Unlock()

	delete(r.next, addr.ByteString())
}
