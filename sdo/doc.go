// SPDX-License-Identifier: MIT

// Package sdo implements the CANopen service data object (CiA 301 7.2.4):
// a [Client] that reads (uploads) and writes (downloads) sub-objects of a
// remote object dictionary, and a [Server] that serves an [od.Dictionary].
// Both support expedited, segmented and block transfer, with the block
// CRC (CRC-16-CCITT, see [CRC16]), block size negotiation (1..127) and the
// upload protocol switch threshold.
//
// # Usage
//
// A device serves its dictionary:
//
//	d, _ := od.Standard(od.Config{NodeID: 5})
//	srv, _ := sdo.NewServer(bus, d)
//	_ = srv.Start()
//	defer srv.Close()
//
// A master reads and writes it:
//
//	c, _ := sdo.NewClient(bus, 5, sdo.WithBlockMode(sdo.BlockAuto))
//	devType, err := c.ReadUint(ctx, 0x1000, 0)
//	err = c.WriteUint16(ctx, 0x1017, 0, 1000) // heartbeat every second
//	var ae *canopen.AbortError
//	if errors.As(err, &ae) && ae.Remote { /* the device refused: ae.Code */ }
//
// # Transfers
//
// The client downloads 1..4 bytes expedited and other sizes (including 0)
// segmented, unless block transfer is selected ([WithBlockMode],
// [UseBlockMode]). Block uploads offer a protocol switch threshold: the
// server answers objects up to that size with a normal upload. Transfers
// on one client are serialized; one server channel runs one transfer at a
// time.
//
// The server checks at the initiate that a download target exists and is
// writable, buffers segmented and block downloads (up to [WithMaxSize],
// beyond it [canopen.AbortOutOfMemory]) and writes them once complete;
// the dictionary's abort code (access, length, range, hooks) reaches the
// client unchanged. A hook error that is not an abort code becomes
// [canopen.AbortGeneral].
//
// # Interpretations
//
// Where CiA 301 leaves room, this package chooses:
//
//   - A new initiate request during a transfer abandons the old transfer
//     silently and starts the new one (a client that timed out and
//     retries is the common cause). Inside a block download's sub-block
//     every message is a segment, so there an initiate cannot be
//     recognized.
//   - Only byte 0 = 0x80 is an abort while block segments may arrive (a
//     last segment reads 0x81..0xFF). Elsewhere every message with
//     specifier 4 is one.
//   - Expedited downloads without the size indicated carry the object's
//     size when it is 1..4 bytes, else 4 bytes.
//   - A size indicated at the initiate that the data does not match is
//     aborted with [canopen.AbortLengthHigh] or [canopen.AbortLengthLow].
//   - The client ignores initiate responses for another object (stale
//     responses of earlier transfers) and waits on.
//   - Block segments out of sequence (lost, repeated, reordered) are
//     ignored; the receiver acknowledges the last segment received in
//     sequence when the block ends, or after a quarter of the timeout
//     without traffic, and the sender resumes after it. When nothing of a
//     block arrived, the receiver repeats its previous message (the
//     acknowledgement or initiate response that was probably lost). While
//     waiting for the end of a block transfer, late segments and repeated
//     acknowledgements are ignored.
//   - Received SDO frames shorter than 8 bytes are zero-padded; frames are
//     always sent with 8 bytes.
//
// # Error detection
//
// Segmented transfers carry no checksum: CiA 301 relies on the CAN CRC.
// A corrupted segment is not detectable by the SDO protocol; repeated
// segments and responses are (the toggle bit), and the transfer is
// aborted with [canopen.AbortToggle]. Block transfers with the CRC
// detect corruption of the data and of the acknowledgements (which
// misplace data) with [canopen.AbortCRC] or a length abort. Under faults
// the protocol therefore either completes with the right data or fails
// with an abort or a timeout, except for corruption in segmented
// transfers, which only the CAN layer prevents. Two limits remain even
// for block transfers: a corrupted command byte can turn a request into
// another valid one that the server executes (the client then reports a
// protocol error, but the server may have acted), and a corrupted
// initiate can disable the CRC or the block protocol for that transfer.
//
// # Timeouts and retries
//
// The client's timeout bounds the wait for each response; the server's
// bounds the pause between requests of a transfer, and aborts it with
// [canopen.AbortTimeout]. With [WithRetries] the client repeats a
// transfer after its own timeout and after a server's timeout abort
// (the server times out first when a request was lost). The client keeps
// one subscription for its lifetime; it discards stale messages before
// each transfer and resubscribes after a failed one.
package sdo
