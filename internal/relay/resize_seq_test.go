// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"testing"

	"reminal/internal/protocol"
)

// A size sent in its place in the output stream carries its seq, and the
// relay forwards it with the seq as it does a data frame's: a viewer puts
// the size where it belongs among the output. Dropped, the size could land
// after output the program wrote for it.
func TestAResizeKeepsItsSeqThroughTheRelay(t *testing.T) {
	g := newParkRig(t)
	a := g.agent()
	v := g.viewer()
	_ = a.WriteJSON(protocol.Message{Type: protocol.TypeResize, Data: "sealed-size", Seq: 7})
	m := g.want(v, protocol.TypeResize)
	if m.Seq != 7 || m.Data != "sealed-size" {
		t.Fatalf("the viewer got seq %d data %q, want seq 7 and the sealed size", m.Seq, m.Data)
	}
}
