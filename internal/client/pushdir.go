// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"

	"github.com/reminal/reminal/internal/protocol"
)

// pushReq is a phone-alert request riding dir_query. It needs no message type
// of its own: dir_query is already reserved to the machine channel, which
// answers only enrolled owners, so the request is owner-only by construction.
//
// The proof on top identifies WHICH owner device asked — every owner shares
// the channel key, so the channel alone cannot say — which is what lets a
// removed owner's phone go quiet. It is bound to the op and the endpoint, so
// a proof for "read my rules" cannot be replayed as "unsubscribe".
type pushReq struct {
	Op    string     `json:"op"` // get | set | remove | test
	Sub   pushSub    `json:"sub"`
	Rules *pushRules `json:"rules,omitempty"`
	Proof ownerProof `json:"proof"`
	// ReqID is echoed back so the page can tell ITS answer from the answers
	// to other viewers' queries, which every viewer on the channel receives.
	ReqID string `json:"req_id,omitempty"`
}

func pushAction(op, endpoint string) string { return "push:" + op + ":" + endpoint }

// applyLocalPush answers one phone-alert request on this machine.
func (a *Agent) applyLocalPush(resp *protocol.DirResponse, q *pushReq) {
	out := &protocol.DirPush{ReqID: q.ReqID}
	resp.Push = out
	if err := validPushSub(q.Sub); err != nil {
		out.Error = err.Error()
		return
	}
	if why := a.verifyOwnerAction(q.Proof, pushAction(q.Op, q.Sub.Endpoint)); why != "" {
		out.Error = why
		return
	}
	switch q.Op {
	case "set":
		rules := defaultPushRules()
		if q.Rules != nil {
			rules = *q.Rules
		}
		rules = rules.clamp()
		if err := upsertPushSub(q.Proof.DevicePub, q.Sub, rules); err != nil {
			out.Error = "could not save: " + err.Error()
			return
		}
	case "remove":
		if err := removePushSub(q.Sub.Endpoint); err != nil {
			out.Error = "could not save: " + err.Error()
			return
		}
	case "test":
		// Synchronous, so the page can say "sent" or say why not — the whole
		// point of the button is finding out whether the path works.
		err := sendPush(q.Sub, pushMessage{
			Title: pushHostLabel(),
			Body:  "Test alert — this machine can reach this device.",
			Tag:   "test", URL: "/",
		})
		if err != nil {
			out.Error = err.Error()
		} else {
			out.TestSent = true
		}
	case "get":
	default:
		out.Error = "unknown request"
		return
	}
	if e, ok := findPushSub(q.Sub.Endpoint); ok {
		out.Subscribed = true
		out.Rules, _ = json.Marshal(e.Rules)
	} else {
		out.Rules, _ = json.Marshal(defaultPushRules())
	}
}
