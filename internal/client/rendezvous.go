package client

// Rendezvous is the end-to-end transfer used by `reminal copy` / `reminal
// paste`. Unlike send/download (scoped to one PTY session's viewers), a
// rendezvous pairs a source on one machine with a paste on any other,
// brokered by a blind relay that only ever sees blinded public keys and
// ciphertext. The short code the user carries between terminals IS the
// shared secret: it blinds the X25519 exchange exactly the way the session
// PIN does in crypto/kex.go, so the relay can neither derive the key nor
// brute-force the code offline (there is no stored ciphertext — the source
// must be online, and a wrong guess is one loud, rate-limited attempt).
//
// Wire flow (frames relayed verbatim, source connects first and waits):
//
//	paste  → KexInit     {Data: blinded paste pubkey, ExID}
//	source → KexResp     {Data: blinded source pubkey, ExID, Wrap: wrapped transfer key}
//	paste  → KexConfirm  {Data: box(label)}            # proves paste has the key
//	source → Data×N      {Data: box(chunk JSON)}       # only after confirm verifies
//	(paste counts chunks; complete when it has Total of them)
//
// The transfer key is a fresh random 256-bit key the source generates and
// wraps under the code-authenticated ECDH; the file (name included) is then
// AES-256-GCM encrypted under it in 256 KB chunks, reusing downloadChunkBytes.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
)

// rendezvousConfirmLabel is the fixed plaintext the paste side encrypts
// under the transfer key as its key-confirmation. Domain-separated so it
// can't be confused with a file chunk.
const rendezvousConfirmLabel = "reminal-copy-confirm-v1"

// errWrongCode is returned on either side when the code-authenticated
// handshake fails to agree on a key — a mistyped code or an active MITM.
var errWrongCode = errors.New("wrong or mistyped code")

// errCodeNotLive is returned to the paste side when the relay reports no
// live source for the code (never existed, already consumed, or expired).
// The CLI renders this as the deliberately-merged "too old or invalid".
var errCodeNotLive = errors.New("code is either too old or invalid")

// frameConn is the minimal transport the handshake needs. The production
// path wraps a relay WebSocket; tests use an in-memory pipe. Keeping the
// exchange transport-agnostic lets us unit-test the whole source<->paste
// round-trip — including wrong-code rejection — without a relay.
type frameConn interface {
	send(protocol.Message) error
	recv() (protocol.Message, error)
}

// rendezvousChunk is the per-chunk payload, encrypted under the transfer
// key before it touches the wire. Same shape as the download chunk so the
// reassembly logic reads familiarly.
type rendezvousChunk struct {
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Name    string `json:"name"`
	Content string `json:"content"` // base64 of this chunk
	Size    int    `json:"size"`    // total file size (informational)
}

// runSource performs the source half of a rendezvous: it waits for the
// paste's KexInit, completes the code-authenticated handshake, verifies the
// paste's key-confirmation, and only then streams the file at path. It
// returns errWrongCode if the peer can't prove the code, so the caller can
// count the failed attempt without ever having leaked file bytes.
func runSource(fc frameConn, code, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("directories aren't supported (try tar first)")
	}

	// 1. Paste opens with KexInit.
	init, err := fc.recv()
	if err != nil {
		return err
	}
	if init.Type == protocol.TypeError {
		// Relay rejected the source (e.g. the code is already in use).
		return errors.New(init.Error)
	}
	if init.Type != protocol.TypeKexInit {
		return fmt.Errorf("rendezvous: expected kex_init, got %q", init.Type)
	}
	exID, err := crypto.ParseExID(init.ExID)
	if err != nil {
		return fmt.Errorf("rendezvous: bad ex_id: %w", err)
	}
	blindedPaste, err := base64.StdEncoding.DecodeString(init.Data)
	if err != nil {
		return fmt.Errorf("rendezvous: bad paste pubkey: %w", err)
	}
	pastePubRaw, err := crypto.UnblindPub(blindedPaste, code)
	if err != nil {
		return err
	}
	pastePub, err := crypto.PeerPublicKey(pastePubRaw)
	if err != nil {
		// A bad point can be a mistyped code mangling the blinded bytes.
		return errWrongCode
	}

	// 2. Our ephemeral half + a fresh transfer key, wrapped under the
	//    code-authenticated ECDH and sent back blinded.
	priv, err := crypto.NewEphemeralKey()
	if err != nil {
		return err
	}
	shared, err := priv.ECDH(pastePub)
	if err != nil {
		return errWrongCode
	}
	transferKey, err := crypto.NewSessionKey()
	if err != nil {
		return err
	}
	wrapped, err := crypto.WrapSessionKey(shared, exID, transferKey)
	if err != nil {
		return err
	}
	blindedSrc, err := crypto.BlindPub(priv.PublicKey().Bytes(), code)
	if err != nil {
		return err
	}
	if err := fc.send(protocol.Message{
		Type: protocol.TypeKexResp,
		Data: base64.StdEncoding.EncodeToString(blindedSrc),
		ExID: init.ExID,
		Wrap: base64.StdEncoding.EncodeToString(wrapped),
	}); err != nil {
		return err
	}

	box, err := crypto.NewBox(transferKey)
	if err != nil {
		return err
	}

	// 3. Verify the paste proved the same key before streaming a single byte.
	confirm, err := fc.recv()
	if err != nil {
		return err
	}
	if confirm.Type != protocol.TypeKexConfirm {
		return fmt.Errorf("rendezvous: expected kex_confirm, got %q", confirm.Type)
	}
	pt, err := box.Decrypt(confirm.Data)
	if err != nil || string(pt) != rendezvousConfirmLabel {
		_ = fc.send(protocol.Message{Type: protocol.TypeError, Error: "bad code"})
		return errWrongCode
	}

	// 4. Stream the file in chunks, each encrypted under the transfer key.
	return streamFile(fc, box, path, int(info.Size()))
}

// streamFile reads path in downloadChunkBytes pieces and sends each as an
// encrypted TypeData frame. Peak memory is ~one chunk. At least one frame
// is sent even for an empty file so the paste always learns (name, total).
func streamFile(fc frameConn, box *crypto.Box, path string, size int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	name := filepath.Base(path)
	total := (size + downloadChunkBytes - 1) / downloadChunkBytes
	if total == 0 {
		total = 1
	}
	buf := make([]byte, downloadChunkBytes)
	for index := 0; index < total; index++ {
		n, rerr := io.ReadFull(f, buf)
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			rerr = nil
		}
		if rerr != nil {
			return rerr
		}
		payload, err := json.Marshal(rendezvousChunk{
			Index:   index,
			Total:   total,
			Name:    name,
			Content: base64.StdEncoding.EncodeToString(buf[:n]),
			Size:    size,
		})
		if err != nil {
			return err
		}
		enc, err := box.Encrypt(payload)
		if err != nil {
			return err
		}
		if err := fc.send(protocol.Message{Type: protocol.TypeData, Data: enc}); err != nil {
			return err
		}
	}
	return nil
}

// runPaste performs the paste half: it initiates the handshake with the
// code, unwraps the transfer key (a failure here means a wrong code),
// confirms, then receives + reassembles the stream and writes it to dest.
// dest "" or "." or an existing directory means "write under that dir with
// the source's filename"; anything else is treated as the target path.
// Returns the written path on success.
func runPaste(fc frameConn, code, dest string) (string, error) {
	priv, err := crypto.NewEphemeralKey()
	if err != nil {
		return "", err
	}
	blinded, err := crypto.BlindPub(priv.PublicKey().Bytes(), code)
	if err != nil {
		return "", err
	}
	exIDHex, exID, err := crypto.NewExID()
	if err != nil {
		return "", err
	}
	if err := fc.send(protocol.Message{
		Type: protocol.TypeKexInit,
		Data: base64.StdEncoding.EncodeToString(blinded),
		ExID: exIDHex,
	}); err != nil {
		return "", err
	}

	resp, err := fc.recv()
	if err != nil {
		return "", err
	}
	if resp.Type == protocol.TypeError {
		// Relay (no live source) or source (rejection) said no.
		return "", errCodeNotLive
	}
	if resp.Type != protocol.TypeKexResp {
		return "", fmt.Errorf("rendezvous: expected kex_resp, got %q", resp.Type)
	}
	blindedSrc, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return "", fmt.Errorf("rendezvous: bad source pubkey: %w", err)
	}
	srcPubRaw, err := crypto.UnblindPub(blindedSrc, code)
	if err != nil {
		return "", err
	}
	srcPub, err := crypto.PeerPublicKey(srcPubRaw)
	if err != nil {
		return "", errWrongCode
	}
	shared, err := priv.ECDH(srcPub)
	if err != nil {
		return "", errWrongCode
	}
	wrapped, err := base64.StdEncoding.DecodeString(resp.Wrap)
	if err != nil {
		return "", fmt.Errorf("rendezvous: bad wrap: %w", err)
	}
	transferKey, err := crypto.UnwrapSessionKey(shared, exID, wrapped)
	if err != nil {
		// The wrap won't open under a key derived from the wrong code.
		return "", errWrongCode
	}
	box, err := crypto.NewBox(transferKey)
	if err != nil {
		return "", err
	}

	// Prove we hold the key, then receive the stream.
	tag, err := box.Encrypt([]byte(rendezvousConfirmLabel))
	if err != nil {
		return "", err
	}
	if err := fc.send(protocol.Message{Type: protocol.TypeKexConfirm, Data: tag}); err != nil {
		return "", err
	}

	name, raw, err := receiveStream(fc, box)
	if err != nil {
		return "", err
	}
	return writeRendezvousFile(dest, name, raw)
}

// receiveStream reads encrypted TypeData chunks until it has Total of them,
// then assembles them in index order. Returns the (source) filename and the
// reassembled bytes.
func receiveStream(fc frameConn, box *crypto.Box) (string, []byte, error) {
	var (
		name   string
		total  = -1
		chunks = map[int][]byte{}
	)
	for total == -1 || len(chunks) < total {
		msg, err := fc.recv()
		if err != nil {
			return "", nil, err
		}
		switch msg.Type {
		case protocol.TypeData:
			pt, err := box.Decrypt(msg.Data)
			if err != nil {
				return "", nil, fmt.Errorf("rendezvous: decrypt chunk: %w", err)
			}
			var c rendezvousChunk
			if err := json.Unmarshal(pt, &c); err != nil {
				return "", nil, fmt.Errorf("rendezvous: parse chunk: %w", err)
			}
			if total == -1 {
				if c.Total <= 0 {
					return "", nil, errors.New("rendezvous: bad chunk total")
				}
				total = c.Total
				name = filepath.Base(c.Name)
			}
			if c.Index < 0 || c.Index >= total {
				return "", nil, fmt.Errorf("rendezvous: bad chunk index %d/%d", c.Index, total)
			}
			b, err := base64.StdEncoding.DecodeString(c.Content)
			if err != nil {
				return "", nil, fmt.Errorf("rendezvous: bad chunk base64: %w", err)
			}
			if _, dup := chunks[c.Index]; !dup {
				chunks[c.Index] = b
			}
		case protocol.TypeError:
			return "", nil, fmt.Errorf("source aborted: %s", msg.Error)
		default:
			// Ignore unrelated frames (pings, etc.).
		}
	}

	totalBytes := 0
	for _, b := range chunks {
		totalBytes += len(b)
	}
	assembled := make([]byte, 0, totalBytes)
	for i := 0; i < total; i++ {
		assembled = append(assembled, chunks[i]...)
	}
	if name == "" {
		name = "pasted-file"
	}
	return name, assembled, nil
}

// writeRendezvousFile resolves dest and writes raw, deduplicating the name
// when writing into a directory. Returns the absolute path written.
func writeRendezvousFile(dest, name string, raw []byte) (string, error) {
	if dest == "" {
		dest = "."
	}
	target := dest
	// If dest is an existing directory (or "."), drop the source filename in it.
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		target = uniquePath(filepath.Join(dest, name))
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}
