// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Rendezvous is the local relay's twin of the Worker's RendezvousRoom
// (cloudflare/src/rendezvous.ts): it brokers one `reminal copy` → `reminal
// paste` transfer per code. It is blind — it pairs a "source" socket with a
// "paste" socket by the code and passes frames between them verbatim; the
// code-authenticated handshake and the file's encryption run end to end, so
// only ciphertext passes and nothing is stored.
//
// For a short code: the source must be online (no stored ciphertext to
// attack offline), a code is burned on the first paste that pairs and when
// the source goes, and a burned, expired or unknown code all get the same
// answer.
type Rendezvous struct {
	mu    sync.Mutex
	rooms map[string]*rvRoom
}

type rvRoom struct {
	source, paste *websocket.Conn
	wmu           [2]sync.Mutex // per peer: gorilla allows one writer at a time
	consumed      bool
	created       time.Time
}

const rvTTL = time.Hour // hard cap on an un-pasted offer

var rvCodeRe = regexp.MustCompile(`^[A-Z0-9]{4,32}$`)

func NewRendezvous() *Rendezvous { return &Rendezvous{rooms: map[string]*rvRoom{}} }

var rvUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// HandleWS serves /rv/{code}/{role}.
func (z *Rendezvous) HandleWS(w http.ResponseWriter, req *http.Request, code, role string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	role = strings.ToLower(role)
	if !rvCodeRe.MatchString(code) || (role != "source" && role != "paste") {
		http.Error(w, "bad rendezvous", http.StatusBadRequest)
		return
	}
	ws, err := rvUpgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	reject := func(closeCode int, reason string) {
		b, _ := json.Marshal(map[string]string{"type": "error", "error": reason})
		_ = ws.WriteMessage(websocket.TextMessage, b)
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, reason), time.Now().Add(time.Second))
		_ = ws.Close()
	}

	z.mu.Lock()
	z.sweepLocked()
	r := z.rooms[code]
	if role == "source" {
		switch {
		case r != nil && r.consumed:
			z.mu.Unlock()
			reject(4404, "code is either too old or invalid")
			return
		case r != nil && r.source != nil:
			z.mu.Unlock()
			reject(4409, "code already in use")
			return
		}
		if r == nil {
			r = &rvRoom{created: time.Now()}
			z.rooms[code] = r
		}
		r.source = ws
	} else {
		switch {
		case r == nil || r.consumed || r.source == nil:
			z.mu.Unlock()
			reject(4404, "code is either too old or invalid")
			return
		case r.paste != nil:
			z.mu.Unlock()
			reject(4409, "a paste is already in progress")
			return
		}
		r.consumed = true // burned on pairing: one live guess per code
		r.paste = ws
	}
	z.mu.Unlock()

	_ = ws.SetReadDeadline(time.Now().Add(rvTTL))
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage && len(data) < 64 && strings.Contains(string(data), `"ping"`) {
			z.write(r, role, websocket.TextMessage, []byte(`{"type":"pong"}`))
			continue
		}
		peer := "source"
		if role == "source" {
			peer = "paste"
		}
		z.write(r, peer, mt, data)
	}

	z.mu.Lock()
	defer z.mu.Unlock()
	if role == "source" {
		// Source gone → the offer is dead; a paste still connected already has
		// everything (the source closes only after the paste's ack).
		r.source = nil
		r.consumed = true
		if r.paste != nil {
			p := r.paste
			go func() {
				_ = p.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "transfer complete"), time.Now().Add(time.Second))
				_ = p.Close()
			}()
		}
	} else {
		r.paste = nil
		// A paste that failed the handshake: tell a source still waiting.
		if r.source != nil {
			go z.write(r, "source", websocket.TextMessage, []byte(`{"type":"error","error":"paste closed"}`))
		}
	}
	// A spent room is kept, consumed, until it ages out, so its code stays spent.
}

// write sends to one side of a room, one writer at a time per side.
func (z *Rendezvous) write(r *rvRoom, role string, mt int, data []byte) {
	i := 0
	if role == "paste" {
		i = 1
	}
	z.mu.Lock()
	c := r.source
	if i == 1 {
		c = r.paste
	}
	z.mu.Unlock()
	if c == nil {
		return
	}
	r.wmu[i].Lock()
	defer r.wmu[i].Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = c.WriteMessage(mt, data)
}

// sweepLocked forgets offers past their lifetime, closing what is left.
func (z *Rendezvous) sweepLocked() {
	for code, r := range z.rooms {
		if time.Since(r.created) < rvTTL {
			continue
		}
		for _, c := range []*websocket.Conn{r.source, r.paste} {
			if c != nil {
				_ = c.Close()
			}
		}
		delete(z.rooms, code)
	}
}
