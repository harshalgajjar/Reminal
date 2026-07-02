// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// chunkPayloads reproduces exactly what broadcastFile emits on the wire:
// one JSON payload per 256 KB chunk, sharing a download_id. Kept in sync
// with control.go's broadcastFile so the round-trip test exercises the
// real frame shape.
func chunkPayloads(t *testing.T, name string, data []byte) [][]byte {
	t.Helper()
	size := len(data)
	total := (size + downloadChunkBytes - 1) / downloadChunkBytes
	if total == 0 {
		total = 1
	}
	out := make([][]byte, 0, total)
	for i := 0; i < total; i++ {
		start := i * downloadChunkBytes
		end := start + downloadChunkBytes
		if end > size {
			end = size
		}
		p, err := json.Marshal(struct {
			DownloadID string `json:"download_id"`
			Index      int    `json:"index"`
			Total      int    `json:"total"`
			Name       string `json:"name"`
			Content    string `json:"content"`
			Size       int    `json:"size"`
		}{
			DownloadID: "deadbeefdeadbeef",
			Index:      i,
			Total:      total,
			Name:       name,
			Content:    base64.StdEncoding.EncodeToString(data[start:end]),
			Size:       size,
		})
		if err != nil {
			t.Fatalf("marshal chunk %d: %v", i, err)
		}
		out = append(out, p)
	}
	return out
}

func TestHandleDownloadReassembles(t *testing.T) {
	// HOME → temp dir so writeIncoming lands somewhere disposable.
	home := t.TempDir()
	t.Setenv("HOME", home)
	incoming := filepath.Join(home, "Downloads", "reminal-incoming")

	// Sizes spanning the chunk boundary: empty, sub-chunk, exact multiples,
	// and off-by-one on either side of a boundary.
	sizes := []int{
		0,
		1,
		downloadChunkBytes - 1,
		downloadChunkBytes,
		downloadChunkBytes + 1,
		3*downloadChunkBytes + 123,
	}

	rng := rand.New(rand.NewSource(42))
	for _, size := range sizes {
		data := make([]byte, size)
		rng.Read(data)

		v := &Viewer{pendingDownloads: make(map[string]*pendingDownload)}
		payloads := chunkPayloads(t, "blob.bin", data)

		// Deliver chunks out of order — reassembly must be index-driven,
		// not arrival-driven. Re-send one chunk mid-transfer (just before
		// the final chunk lands) to exercise the in-flight dedup guard.
		order := rng.Perm(len(payloads))
		for i, idx := range order {
			if len(order) > 1 && i == len(order)-1 {
				v.handleDownload(payloads[order[0]]) // duplicate, must be ignored
			}
			v.handleDownload(payloads[idx])
		}

		// Exactly one file should exist, byte-identical to the input.
		entries, err := os.ReadDir(incoming)
		if err != nil {
			t.Fatalf("size %d: read incoming dir: %v", size, err)
		}
		if len(entries) != 1 {
			t.Fatalf("size %d: expected 1 file, found %d: %v", size, len(entries), entries)
		}
		got, err := os.ReadFile(filepath.Join(incoming, entries[0].Name()))
		if err != nil {
			t.Fatalf("size %d: read result: %v", size, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("size %d: reassembled bytes differ (got %d, want %d)", size, len(got), len(data))
		}
		if v.pendingDownloads["deadbeefdeadbeef"] != nil {
			t.Fatalf("size %d: pending entry not cleaned up after completion", size)
		}

		// Clean up for the next size so the "exactly 1 file" check holds.
		if err := os.RemoveAll(incoming); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}
