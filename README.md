# chunk2json

`chunk2json` reads Cardano ImmutableDB chunk files (the `immutable/NNNNN.chunk`,
`.primary` and `.secondary` triplets written by cardano-node and distributed in
Mithril snapshots) and prints every block as one line of JSON.

By default each block is rendered as a readable object graph: a header and a
list of transactions, each with named fields, bech32 addresses, `txid#index`
inputs, assets, certificates, governance actions, witnesses and metadata. Two
raw views are available for when the exact wire form matters.

```sh
go install github.com/chrisguiney/chunk2json@latest

chunk2json db/immutable/10353.chunk | jq . | less
chunk2json -wire db/immutable/10353.chunk | jq -c '.block.transactions[0]'
chunk2json -no-body db/immutable/*.chunk | jq -c '{slot, era, tx_count}'
```

## Getting chunk files

Any cardano-node `db/immutable` directory works. To fetch one from Mithril
without the Mithril client, read the latest Cardano database artifact from an
aggregator, then download and extract the per-file archives it lists. For
Preview:

```sh
AGG=https://aggregator.pre-release-preview.api.mithril.network/aggregator
HASH=$(curl -s $AGG/artifact/cardano-database | jq -r '.[0].hash')
ART=$(curl -s $AGG/artifact/cardano-database/$HASH)
echo "$ART" | jq '.beacon'   # epoch and last immutable file number
TEMPLATE=$(echo "$ART" | jq -r '.immutables.locations[0].uri.Template')

# Immutable file N is a zstd tarball of immutable/NNNNN.{chunk,primary,secondary};
# names are zero-padded to five digits.
curl -s "${TEMPLATE/\{immutable_file_number\}/00000}" | zstd -dc | tar xf -
```

This downloads data without verifying the Mithril certificate; use the
Mithril client when the data must be authenticated.

## Output

Each line is one block, in chunk order:

| Field | Meaning |
|---|---|
| `chunk`, `index` | chunk name and the block's position in it |
| `slot`, `hash` | from the secondary index |
| `era_tag` | hard-fork-combinator era tag from the chunk's `[era, block]` wrapper |
| `is_ebb` | as reported by dingo's ImmutableDB reader; the pinned dingo version guesses from slot numbers, so it reports the first block of a chain that starts after Byron as an EBB |
| `block_offset`, `block_size` | byte range of the block in the `.chunk` file |
| `header_offset`, `header_size` | header range, relative to the block |
| `checksum` | CRC32 recorded in the secondary index (not re-verified) |
| `cbor_size` | size of the block CBOR without the 2-byte era wrapper |
| `era`, `block_number`, `tx_count` | from decoding the block with gouroboros |
| `decode_error` | present if the block could not be decoded |
| `block` | the block body, in the format chosen by the flags below |

### Body formats

- **default — struct view.** The types in [`view.go`](view.go), filled from
  gouroboros's ledger types. Transactions are single records rather than the
  parallel body, witness and auxiliary-data arrays of the wire format.
  Amounts are exact JSON numbers. Byte strings are hex; transaction metadata
  follows cardano-cli's no-schema JSON, so metadata bytes are `"0x…"`.
- **`-wire`** — the raw CBOR with CDDL field names (Shelley through Conway),
  in wire order, with nothing dropped. Unrecognised keys appear as `key_N` and
  surplus positions as `field_N`.
- **`-generic`** — the raw CBOR as plain JSON with no labels: maps become
  objects or `[{"k","v"}]` lists, tags become `{"@tag","@value"}`.

In every format, a subtree nested more than 4,096 CBOR levels deep is printed
as `{"@too_deep_cbor": "<hex>"}` rather than expanded, so each line stays
within the 10,000-level limit of common JSON parsers (encoding/json, jq).
Transaction metadata on Preview reaches 16,056 levels. Inputs deeper than
gouroboros's decoder limit (`cbor.MaxNestedLevels`, 16,384) are reported in
`decode_error`.

Pool relay IPv6 addresses are printed in standard notation. The ledger
serialises them as four little-endian 32-bit words, which chunk2json
reorders.

Other flags: `-no-body` omits `block`, `-cbor` adds the block CBOR as hex,
`-no-ledger` skips the gouroboros decode (and so `era`, `block_number`,
`tx_count` and transaction ids) when the struct view is not being printed.

### What the struct view does not show

The struct view shows what gouroboros decodes, so it inherits its
normalisation. Use `-wire` when these matter:

- assets and mint entries with a quantity of zero are dropped;
- duplicate redeemer keys are merged;
- Go map order is not wire order, so votes, withdrawals, MIR rewards and
  committee changes are sorted instead;
- a pool registration's reward account is shown as its key hash, since
  gouroboros does not keep the address header byte;
- Byron blocks get only what gouroboros's era-independent interfaces expose.

## How it works

Block iteration — primary index to secondary index to chunk — uses
[dingo](https://github.com/blinklabs-io/dingo)'s `database/immutable`
package. Each block is then decoded with
[gouroboros](https://github.com/blinklabs-io/gouroboros). The secondary index
fields are not exported by dingo, so chunk2json re-reads the 56-byte
secondary records itself and checks each against the block dingo returned by
hash.

The CBOR walker used by `-wire` and `-generic` is in [`cbor.go`](cbor.go); the
CDDL labels are in [`labels.go`](labels.go).

## Validation

The struct view is checked against `-wire` by transforming each wire record
into the struct view it should produce and comparing field by field; any wire
field the transform does not recognise is reported. On a Preview snapshot
(epoch 1477, immutable file 29556) every difference found was one of the
normalisations listed above.

## License

Apache License 2.0; see [LICENSE](LICENSE). dingo, gouroboros and plutigo
are also Apache-2.0, so binary distributions must carry their notices.
