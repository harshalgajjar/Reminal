// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// The relay once maintained two hand-written forward whitelists that drifted:
// the legacy handler was missing new_session (the Machines-panel "+"), the window
// and app controls, and the WebRTC signalling — so those silently dropped on the
// local relay. forwardableTypes is now the single source both handlers use; this
// test guards the types whose omission would break a whole feature over the local
// relay, so a careless edit fails loudly instead of shipping a silent drop.
func TestForwardableTypesCoverAgentViewerFeatures(t *testing.T) {
	required := []protocol.MessageType{
		// core session I/O + key exchange
		protocol.TypeData, protocol.TypeResize, protocol.TypeResume,
		protocol.TypeKexInit, protocol.TypeKexResp,
		// ownership / directory channel + PIN-free connect + spawn
		protocol.TypeOwnerInit, protocol.TypeOwnerResp,
		protocol.TypeDirQuery, protocol.TypeDirResp, protocol.TypeDirRename,
		protocol.TypeDirRevokeSelf, protocol.TypeDirKill, protocol.TypeNewSession,
		// window mirroring + app control
		protocol.TypeWindowList, protocol.TypeWindowCtl, protocol.TypeWindowFrame,
		protocol.TypeWindowInput, protocol.TypeWindowAck, protocol.TypeWindowClose,
		protocol.TypeAppList, protocol.TypeAppOpen, protocol.TypeHostInfo,
		// WebRTC signalling
		protocol.TypeWebRTCHello, protocol.TypeWebRTCOffer,
		protocol.TypeWebRTCAnswer, protocol.TypeWebRTCICE,
	}
	for _, mt := range required {
		if !forwardableTypes[mt] {
			t.Errorf("forwardableTypes is missing %q — the relay would silently drop it "+
				"(both connection handlers depend on this set)", mt)
		}
	}
}
