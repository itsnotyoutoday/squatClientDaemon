// Copyright 2026 The squatClientDaemon authors.
// Use of this source code is governed by a BSD-style license (see LICENSE in
// this directory); this file is NEW code added to the vendored Go crypto/tls.

package tls

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
)

// Serialized form of MigrationState (all integers big endian):
//
//	"SQTLSMIG" | u16 format | body | sha256(everything before)
//
// body, format 1: u8 flags(bit0 isClient, bit1 didResume) | u16 version | u16 suite |
// dir(In) | dir(Out) | bytes RawInput | bytes Plaintext | bytes ServerName |
// bytes ALPN | u16 n | n * bytes cert
// dir: [8]seq | bytes Secret | bytes Key | bytes IV       (bytes = u32 len | data)
//
// The trailing digest only detects truncation and bit rot; it is not
// authentication (the file is trusted, mode 0600). Fields may only be appended
// within a format; anything else bumps MigrationFormat.

const (
	migrationMagic = "SQTLSMIG"
	// MigrationFormat is the serialization format written by MarshalBinary.
	MigrationFormat = 1
)

func putBytes(b *bytes.Buffer, p []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(p)))
	b.Write(l[:])
	b.Write(p)
}

// MarshalBinary serializes the state. The result holds key material; the caller
// should clear it (clear(b)) once written.
func (s *MigrationState) MarshalBinary() ([]byte, error) {
	if len(s.PeerCertificates) > 0xffff {
		return nil, errors.New("tls: too many certificates")
	}
	var b bytes.Buffer
	b.WriteString(migrationMagic)
	b.Write([]byte{0, MigrationFormat})
	var flags byte
	if s.IsClient {
		flags |= 1
	}
	if s.DidResume {
		flags |= 2
	}
	b.WriteByte(flags)
	var u [2]byte
	binary.BigEndian.PutUint16(u[:], s.Version)
	b.Write(u[:])
	binary.BigEndian.PutUint16(u[:], s.CipherSuite)
	b.Write(u[:])
	for _, d := range []*MigrationDir{&s.In, &s.Out} {
		b.Write(d.Seq[:])
		putBytes(&b, d.Secret)
		putBytes(&b, d.Key)
		putBytes(&b, d.IV)
	}
	putBytes(&b, s.RawInput)
	putBytes(&b, s.Plaintext)
	putBytes(&b, []byte(s.ServerName))
	putBytes(&b, []byte(s.NegotiatedProtocol))
	binary.BigEndian.PutUint16(u[:], uint16(len(s.PeerCertificates)))
	b.Write(u[:])
	for _, c := range s.PeerCertificates {
		putBytes(&b, c)
	}
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes(), nil
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.b) {
		r.err = errors.New("tls: migration state: truncated")
		return nil
	}
	p := r.b[:n]
	r.b = r.b[n:]
	return p
}

func (r *reader) u16() uint16 {
	if p := r.take(2); p != nil {
		return binary.BigEndian.Uint16(p)
	}
	return 0
}

func (r *reader) bytes() []byte {
	p := r.take(4)
	if p == nil {
		return nil
	}
	n := binary.BigEndian.Uint32(p)
	if n > maxMigrateRaw {
		r.err = errors.New("tls: migration state: field too large")
		return nil
	}
	v := r.take(int(n))
	if len(v) == 0 {
		return nil
	}
	return bytes.Clone(v)
}

// UnmarshalMigrationState parses MarshalBinary output, rejecting truncated or
// corrupted data and unknown formats.
func UnmarshalMigrationState(data []byte) (*MigrationState, error) {
	if len(data) < len(migrationMagic)+2+sha256.Size || string(data[:len(migrationMagic)]) != migrationMagic {
		return nil, errors.New("tls: migration state: bad magic")
	}
	body, sum := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	want := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(want[:], sum) != 1 {
		return nil, errors.New("tls: migration state: checksum mismatch")
	}
	r := &reader{b: body[len(migrationMagic):]}
	if f := r.u16(); f != MigrationFormat {
		return nil, fmt.Errorf("tls: migration state: unsupported format %d", f)
	}
	s := &MigrationState{}
	if p := r.take(1); p != nil {
		s.IsClient, s.DidResume = p[0]&1 != 0, p[0]&2 != 0
	}
	s.Version = r.u16()
	s.CipherSuite = r.u16()
	for _, d := range []*MigrationDir{&s.In, &s.Out} {
		if p := r.take(8); p != nil {
			copy(d.Seq[:], p)
		}
		d.Secret, d.Key, d.IV = r.bytes(), r.bytes(), r.bytes()
	}
	s.RawInput = r.bytes()
	s.Plaintext = r.bytes()
	s.ServerName = string(r.bytes())
	s.NegotiatedProtocol = string(r.bytes())
	n := int(r.u16())
	for i := 0; i < n && r.err == nil; i++ {
		s.PeerCertificates = append(s.PeerCertificates, r.bytes())
	}
	if r.err != nil {
		return nil, r.err
	}
	// Bytes left over are fields a newer writer appended within format 1: ignored by contract.
	return s, nil
}
