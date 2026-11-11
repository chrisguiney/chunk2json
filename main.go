// chunk2json prints every block of one or more ImmutableDB chunks as
// newline-delimited JSON, one object per block. The block body defaults to
// the struct view in view.go; -wire and -generic show the raw CBOR instead.
//
// Block iteration (primary -> secondary -> chunk) is dingo's
// database/immutable reader. That package does not export the secondary
// index fields, so the offsets in the output come from re-reading the
// 56-byte secondary records here; each record is matched to the block the
// library returned by hash, and a mismatch is a hard error.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blinklabs-io/dingo/database/immutable"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

const secondaryEntrySize = 56

type secondaryEntry struct {
	BlockOffset  uint64
	HeaderOffset uint16
	HeaderSize   uint16
	Checksum     uint32
	HeaderHash   [32]byte
	BlockOrEbb   uint64
}

type record struct {
	Chunk        string `json:"chunk"`
	Index        int    `json:"index"`
	Slot         uint64 `json:"slot"`
	Hash         string `json:"hash"`
	EraTag       uint   `json:"era_tag"`
	IsEbb        bool   `json:"is_ebb"`
	BlockOffset  uint64 `json:"block_offset"`
	BlockSize    uint64 `json:"block_size"`
	HeaderOffset uint16 `json:"header_offset"`
	HeaderSize   uint16 `json:"header_size"`
	Checksum     string `json:"checksum"`
	CborSize     int    `json:"cbor_size"`
	Era          string `json:"era,omitempty"`
	BlockNumber  uint64 `json:"block_number,omitempty"`
	TxCount      *int   `json:"tx_count,omitempty"`
	DecodeError  string `json:"decode_error,omitempty"`
	Cbor         string `json:"cbor,omitempty"`
	Block        any    `json:"block,omitempty"`
}

func main() {
	noBody := flag.Bool("no-body", false, "omit the block body (header and transactions)")
	withCbor := flag.Bool("cbor", false, "include the block CBOR as hex")
	noLedger := flag.Bool("no-ledger", false, "skip gouroboros decoding (era, block_number, tx_count, transaction ids)")
	wire := flag.Bool("wire", false, "emit the body as raw CBOR labelled with CDDL field names (wire order, nothing dropped)")
	generic := flag.Bool("generic", false, "emit the body as unlabelled positional CBOR")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <immutable-dir>/NNNNN[.chunk] ...\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer out.Flush()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for _, arg := range flag.Args() {
		if err := dumpChunk(arg, enc, !*noBody, *withCbor, !*noLedger, *wire, *generic); err != nil {
			out.Flush()
			fmt.Fprintf(os.Stderr, "%s: %v\n", arg, err)
			os.Exit(1)
		}
	}
}

func dumpChunk(arg string, enc *json.Encoder, body, withCbor, decodeLedger, wire, generic bool) error {
	dir := filepath.Dir(arg)
	name := strings.TrimSuffix(filepath.Base(arg), filepath.Ext(arg))
	base := filepath.Join(dir, name)

	// The library reads a whole directory; give it one holding only this
	// chunk's triplet so the iterator yields exactly this chunk.
	tmp, err := os.MkdirTemp("", "chunk2json-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, ext := range []string{".chunk", ".primary", ".secondary"} {
		src, err := filepath.Abs(base + ext)
		if err != nil {
			return err
		}
		if _, err := os.Stat(src); err != nil {
			return err
		}
		if err := os.Symlink(src, filepath.Join(tmp, name+ext)); err != nil {
			return err
		}
	}

	secBytes, err := os.ReadFile(base + ".secondary")
	if err != nil {
		return err
	}
	if len(secBytes)%secondaryEntrySize != 0 {
		return fmt.Errorf("secondary size %d not a multiple of %d", len(secBytes), secondaryEntrySize)
	}
	entries := make([]secondaryEntry, len(secBytes)/secondaryEntrySize)
	if err := binary.Read(bytes.NewReader(secBytes), binary.BigEndian, entries); err != nil {
		return err
	}
	chunkInfo, err := os.Stat(base + ".chunk")
	if err != nil {
		return err
	}

	db, err := immutable.New(tmp)
	if err != nil {
		return err
	}
	iter, err := db.BlocksFromPoint(ocommon.NewPointOrigin())
	if err != nil {
		return err
	}
	defer iter.Close()

	for idx := 0; ; idx++ {
		blk, err := iter.Next()
		if err != nil {
			return fmt.Errorf("block %d: %w", idx, err)
		}
		if blk == nil {
			if idx != len(entries) {
				return fmt.Errorf("library returned %d blocks, secondary has %d records", idx, len(entries))
			}
			return nil
		}
		if idx >= len(entries) {
			return fmt.Errorf("library returned more blocks than the %d secondary records", len(entries))
		}
		e := entries[idx]
		if !bytes.Equal(e.HeaderHash[:], blk.Hash) {
			return fmt.Errorf("block %d: secondary hash %x != library hash %x", idx, e.HeaderHash, blk.Hash)
		}
		end := uint64(chunkInfo.Size())
		if idx+1 < len(entries) {
			end = entries[idx+1].BlockOffset
		}
		rec := record{
			Chunk:        name,
			Index:        idx,
			Slot:         blk.Slot,
			Hash:         hex.EncodeToString(blk.Hash),
			EraTag:       blk.Type,
			IsEbb:        blk.IsEbb,
			BlockOffset:  e.BlockOffset,
			BlockSize:    end - e.BlockOffset,
			HeaderOffset: e.HeaderOffset,
			HeaderSize:   e.HeaderSize,
			Checksum:     fmt.Sprintf("%08x", e.Checksum),
			CborSize:     len(blk.Cbor),
		}
		var txHashes []string
		var lb ledger.Block
		if decodeLedger || (body && !wire && !generic) {
			lb, err = ledger.NewBlockFromCbor(blk.Type, blk.Cbor, lcommon.VerifyConfig{SkipBodyHashValidation: true})
			if err != nil {
				rec.DecodeError = err.Error()
			} else {
				rec.Era = lb.Era().Name
				rec.BlockNumber = lb.BlockNumber()
				txs := lb.Transactions()
				n := len(txs)
				rec.TxCount = &n
				for _, tx := range txs {
					h := tx.Hash()
					txHashes = append(txHashes, hex.EncodeToString(h[:]))
				}
			}
		}
		if withCbor {
			rec.Cbor = hex.EncodeToString(blk.Cbor)
		}
		if body && !wire && !generic {
			if lb != nil {
				bv, err := buildBlock(lb)
				if err != nil {
					rec.DecodeError = strings.TrimPrefix(rec.DecodeError+"; view: "+err.Error(), "; ")
				} else {
					rec.Block = bv
				}
			}
		} else if body {
			v, err := decodeCBOR(blk.Cbor)
			switch {
			case err != nil:
				rec.DecodeError = strings.TrimPrefix(rec.DecodeError+"; cbor: "+err.Error(), "; ")
			case generic:
				rec.Block = v
			default:
				rec.Block = labelBlock(blk.Type, v, txHashes)
			}
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
	}
}
