// Copyright 2020 The Penguin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handshake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/penguintop/penguin/pkg/crypto"
	"github.com/penguintop/penguin/pkg/logging"
	"github.com/penguintop/penguin/pkg/p2p"
	"github.com/penguintop/penguin/pkg/p2p/libp2p/internal/handshake/pb"
	"github.com/penguintop/penguin/pkg/p2p/protobuf"
	"github.com/penguintop/penguin/pkg/pen"
    "github.com/penguintop/penguin/pkg/penguin"

	"github.com/libp2p/go-libp2p-core/network"
	libp2ppeer "github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// ProtocolName is the text of the name of the handshake protocol.
	ProtocolName = "handshake"
	// ProtocolVersion is the current handshake protocol version.
	ProtocolVersion = "3.0.0"
	// StreamName is the name of the stream used for handshake purposes.
	StreamName = "handshake"
	// MaxWelcomeMessageLength is maximum number of characters allowed in the welcome message.
	MaxWelcomeMessageLength = 140
	handshakeTimeout        = 15 * time.Second
)

var (
	// ErrNetworkIDIncompatible is returned if response from the other peer does not have valid networkID.
	ErrNetworkIDIncompatible = errors.New("incompatible network ID")

	// ErrHandshakeDuplicate is returned  if the handshake response has been received by an already processed peer.
	ErrHandshakeDuplicate = errors.New("duplicate handshake")

	// ErrInvalidAck is returned if data in received in ack is not valid (invalid signature for example).
	ErrInvalidAck = errors.New("invalid ack")

	// ErrInvalidSyn is returned if observable address in ack is not a valid..
	ErrInvalidSyn = errors.New("invalid syn")

	// ErrAddressNotFound is returned if observable address in ack is not a valid..
	ErrAddressNotFound = errors.New("address not found")

	// ErrWelcomeMessageLength is returned if the welcome message is longer than the maximum length
	ErrWelcomeMessageLength = fmt.Errorf("handshake welcome message longer than maximum of %d characters", MaxWelcomeMessageLength)
)

// AdvertisableAddressResolver can Resolve a Multiaddress.
type AdvertisableAddressResolver interface {
	Resolve(observedAdddress ma.Multiaddr) (ma.Multiaddr, error)
}

type SenderMatcher interface {
	Matches(ctx context.Context, tx []byte, networkID uint64, senderOverlay penguin.Address) (bool, error)
}

// Service can perform initiate or handle a handshake between peers.
type Service struct {
	signer                crypto.Signer
	advertisableAddresser AdvertisableAddressResolver
	senderMatcher         SenderMatcher
	overlay               penguin.Address
	fullNode              bool
	transaction           []byte
	networkID             uint64
	welcomeMessage        atomic.Value
	receivedHandshakes    map[libp2ppeer.ID]struct{}
	receivedHandshakesMu  sync.Mutex
	logger                logging.Logger

	network.Notifiee // handshake service can be the receiver for network.Notify
}

// Info contains the information received from the handshake.
type Info struct {
	PenAddress *pen.Address
	FullNode   bool
}

func (i *Info) LightString() string {
	if !i.FullNode {
		return " (light)"
	}

	return ""
}

// New creates a new handshake Service.
func New(signer crypto.Signer, advertisableAddresser AdvertisableAddressResolver, isSender SenderMatcher, overlay penguin.Address, networkID uint64, fullNode bool, transaction []byte, welcomeMessage string, logger logging.Logger) (*Service, error) {
	if len(welcomeMessage) > MaxWelcomeMessageLength {
		return nil, ErrWelcomeMessageLength
	}

	svc := &Service{
		signer:                signer,
		advertisableAddresser: advertisableAddresser,
		overlay:               overlay,
		networkID:             networkID,
		fullNode:              fullNode,
		transaction:           transaction,
		senderMatcher:         isSender,
		receivedHandshakes:    make(map[libp2ppeer.ID]struct{}),
		logger:                logger,
		Notifiee:              new(network.NoopNotifiee),
	}
	svc.welcomeMessage.Store(welcomeMessage)

	return svc, nil
}

// Handshake initiates a handshake with a peer.
func (s *Service) Handshake(ctx context.Context, stream p2p.Stream, peerMultiaddr ma.Multiaddr, peerID libp2ppeer.ID) (i *Info, err error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	w, r := protobuf.NewWriterAndReader(stream)
	fullRemoteMA, err := buildFullMA(peerMultiaddr, peerID)
	if err != nil {
		return nil, err
	}

	fullRemoteMABytes, err := fullRemoteMA.MarshalBinary()
	if err != nil {
		return nil, err
	}

	if err := w.WriteMsgWithContext(ctx, &pb.Syn{
		ObservedUnderlay: fullRemoteMABytes,
	}); err != nil {
		return nil, fmt.Errorf("write syn message: %w", err)
	}

	var resp pb.SynAck
	if err := r.ReadMsgWithContext(ctx, &resp); err != nil {
		return nil, fmt.Errorf("read synack message: %w", err)
	}

	remotePenAddress, err := s.parseCheckAck(resp.Ack)
	if err != nil {
		return nil, err
	}

	observedUnderlay, err := ma.NewMultiaddrBytes(resp.Syn.ObservedUnderlay)
	if err != nil {
		return nil, ErrInvalidSyn
	}

	advertisableUnderlay, err := s.advertisableAddresser.Resolve(observedUnderlay)
	if err != nil {
		return nil, err
	}

	penAddress, err := pen.NewAddress(s.signer, advertisableUnderlay, s.overlay, s.networkID)
	if err != nil {
		return nil, err
	}

	advertisableUnderlayBytes, err := penAddress.Underlay.MarshalBinary()
	if err != nil {
		return nil, err
	}

	// Synced read:
	welcomeMessage := s.GetWelcomeMessage()
	if err := w.WriteMsgWithContext(ctx, &pb.Ack{
		Address: &pb.PenAddress{
			Underlay:  advertisableUnderlayBytes,
			Overlay:   penAddress.Overlay.Bytes(),
			Signature: penAddress.Signature,
		},
		NetworkID:      s.networkID,
		FullNode:       s.fullNode,
		Transaction:    s.transaction,
		WelcomeMessage: welcomeMessage,
	}); err != nil {
		return nil, fmt.Errorf("write ack message: %w", err)
	}

	s.logger.Tracef("handshake finished for peer (outbound) %s", remotePenAddress.Overlay.String())
	if len(resp.Ack.WelcomeMessage) > 0 {
		s.logger.Infof("greeting \"%s\" from peer: %s", resp.Ack.WelcomeMessage, remotePenAddress.Overlay.String())
	}

	return &Info{
		PenAddress: remotePenAddress,
		FullNode:   resp.Ack.FullNode,
	}, nil
}

// Handle handles an incoming handshake from a peer.
func (s *Service) Handle(ctx context.Context, stream p2p.Stream, remoteMultiaddr ma.Multiaddr, remotePeerID libp2ppeer.ID) (i *Info, err error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	s.receivedHandshakesMu.Lock()
	if _, exists := s.receivedHandshakes[remotePeerID]; exists {
		s.receivedHandshakesMu.Unlock()
		return nil, ErrHandshakeDuplicate
	}

	s.receivedHandshakes[remotePeerID] = struct{}{}
	s.receivedHandshakesMu.Unlock()
	w, r := protobuf.NewWriterAndReader(stream)
	fullRemoteMA, err := buildFullMA(remoteMultiaddr, remotePeerID)
	if err != nil {
		return nil, err
	}

	fullRemoteMABytes, err := fullRemoteMA.MarshalBinary()
	if err != nil {
		return nil, err
	}

	var syn pb.Syn
	if err := r.ReadMsgWithContext(ctx, &syn); err != nil {
		return nil, fmt.Errorf("read syn message: %w", err)
	}

	observedUnderlay, err := ma.NewMultiaddrBytes(syn.ObservedUnderlay)
	if err != nil {
		return nil, ErrInvalidSyn
	}

	advertisableUnderlay, err := s.advertisableAddresser.Resolve(observedUnderlay)
	if err != nil {
		return nil, err
	}

	penAddress, err := pen.NewAddress(s.signer, advertisableUnderlay, s.overlay, s.networkID)
	if err != nil {
		return nil, err
	}

	advertisableUnderlayBytes, err := penAddress.Underlay.MarshalBinary()
	if err != nil {
		return nil, err
	}

	welcomeMessage := s.GetWelcomeMessage()

	if err := w.WriteMsgWithContext(ctx, &pb.SynAck{
		Syn: &pb.Syn{
			ObservedUnderlay: fullRemoteMABytes,
		},
		Ack: &pb.Ack{
			Address: &pb.PenAddress{
				Underlay:  advertisableUnderlayBytes,
				Overlay:   penAddress.Overlay.Bytes(),
				Signature: penAddress.Signature,
			},
			NetworkID:      s.networkID,
			FullNode:       s.fullNode,
			Transaction:    s.transaction,
			WelcomeMessage: welcomeMessage,
		},
	}); err != nil {
		return nil, fmt.Errorf("write synack message: %w", err)
	}

	var ack pb.Ack
	if err := r.ReadMsgWithContext(ctx, &ack); err != nil {
		return nil, fmt.Errorf("read ack message: %w", err)
	}

	remotePenAddress, err := s.parseCheckAck(&ack)
	if err != nil {
		return nil, err
	}

	s.logger.Tracef("handshake finished for peer (inbound) %s", remotePenAddress.Overlay.String())
	if len(ack.WelcomeMessage) > 0 {
		s.logger.Infof("greeting \"%s\" from peer: %s", ack.WelcomeMessage, remotePenAddress.Overlay.String())
	}

	matchesSender, err := s.senderMatcher.Matches(ctx, ack.Transaction, s.networkID, remotePenAddress.Overlay)
	if err != nil {
		return nil, err
	}

	if !matchesSender {
		return nil, fmt.Errorf("given address is not registered on Ethereum: %v: %w", remotePenAddress.Overlay, ErrAddressNotFound)
	}

	return &Info{
		PenAddress: remotePenAddress,
		FullNode:   ack.FullNode,
	}, nil
}

// Disconnected is called when the peer disconnects.
func (s *Service) Disconnected(_ network.Network, c network.Conn) {
	s.receivedHandshakesMu.Lock()
	defer s.receivedHandshakesMu.Unlock()
	delete(s.receivedHandshakes, c.RemotePeer())
}

// SetWelcomeMessage sets the new handshake welcome message.
func (s *Service) SetWelcomeMessage(msg string) (err error) {
	if len(msg) > MaxWelcomeMessageLength {
		return ErrWelcomeMessageLength
	}
	s.welcomeMessage.Store(msg)
	return nil
}

// GetWelcomeMessage returns the the current handshake welcome message.
func (s *Service) GetWelcomeMessage() string {
	return s.welcomeMessage.Load().(string)
}

func buildFullMA(addr ma.Multiaddr, peerID libp2ppeer.ID) (ma.Multiaddr, error) {
	return ma.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", addr.String(), peerID.Pretty()))
}

func (s *Service) parseCheckAck(ack *pb.Ack) (*pen.Address, error) {
	if ack.NetworkID != s.networkID {
		return nil, ErrNetworkIDIncompatible
	}

	penAddress, err := pen.ParseAddress(ack.Address.Underlay, ack.Address.Overlay, ack.Address.Signature, s.networkID)
	if err != nil {
		return nil, ErrInvalidAck
	}

	return penAddress, nil
}
