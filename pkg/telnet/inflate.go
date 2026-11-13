package telnet

import (
	"errors"
	"sync"
)

// inflater is a resumable zlib (RFC 1950/1951) decompressor. compress/flate
// cannot be resumed after running out of input (its errors are sticky) and a
// goroutine-free core cannot block on a reader, so MCCP2 streams, which arrive
// in arbitrary TCP-sized pieces, are decoded by this small state machine. Every
// decoding step is transactional: if the buffered input ends in the middle of a
// step the bit position is rolled back and the step is retried on the next feed.

var errInflate = errors.New("telnet: corrupt zlib stream")

const (
	izHeader = iota
	izBlockHeader
	izStored
	izCodes
	izTrailer
	izDone
)

type huffman struct {
	count  [16]uint16
	symbol []uint16
}

func newHuffman(lengths []uint8) (*huffman, bool) {
	h := &huffman{symbol: make([]uint16, len(lengths))}
	for _, l := range lengths {
		h.count[l]++
	}
	if int(h.count[0]) == len(lengths) {
		return h, true // no codes: legal for an unused distance tree
	}
	left := 1
	for l := 1; l <= 15; l++ {
		left <<= 1
		left -= int(h.count[l])
		if left < 0 {
			return nil, false // over-subscribed
		}
	}
	var offs [16]uint16
	for l := 1; l < 15; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	for sym, l := range lengths {
		if l != 0 {
			h.symbol[offs[l]] = uint16(sym)
			offs[l]++
		}
	}
	return h, true // incomplete codes are tolerated (decode fails only if hit)
}

var fixedTables = sync.OnceValues(func() (*huffman, *huffman) {
	var l [288]uint8
	for i := range l {
		switch {
		case i < 144:
			l[i] = 8
		case i < 256:
			l[i] = 9
		case i < 280:
			l[i] = 7
		default:
			l[i] = 8
		}
	}
	lit, _ := newHuffman(l[:])
	var d [30]uint8
	for i := range d {
		d[i] = 5
	}
	dist, _ := newHuffman(d[:])
	return lit, dist
})

var (
	lenBase  = [29]uint16{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	lenExtra = [29]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	dstBase  = [30]uint16{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	dstExtra = [30]uint8{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
	clOrder  = [19]uint8{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}
)

type inflater struct {
	in     []byte // unconsumed input
	pos    int    // next byte of in
	bitBuf uint32
	bitCnt uint
	state  int
	final  bool
	lit    *huffman
	dist   *huffman
	stored int // remaining stored bytes
	hist   []byte
	adl    adlerAcc
}

type adlerAcc struct{ a, b uint32 }

func (x *adlerAcc) reset() { x.a, x.b = 1, 0 }
func (x *adlerAcc) write(p []byte) {
	const mod = 65521
	a, b := x.a, x.b
	for len(p) > 0 {
		n := len(p)
		if n > 5552 {
			n = 5552
		}
		for _, c := range p[:n] {
			a += uint32(c)
			b += a
		}
		a %= mod
		b %= mod
		p = p[n:]
	}
	x.a, x.b = a, b
}
func (x *adlerAcc) sum32() uint32 { return x.b<<16 | x.a }

func newInflater() *inflater {
	z := &inflater{state: izHeader}
	z.adl.reset()
	return z
}

type snapshot struct {
	pos    int
	bitBuf uint32
	bitCnt uint
}

func (z *inflater) snap() snapshot     { return snapshot{z.pos, z.bitBuf, z.bitCnt} }
func (z *inflater) restore(s snapshot) { z.pos, z.bitBuf, z.bitCnt = s.pos, s.bitBuf, s.bitCnt }

var errNeed = errors.New("need more input")

func (z *inflater) bits(n uint) (uint32, error) {
	for z.bitCnt < n {
		if z.pos >= len(z.in) {
			return 0, errNeed
		}
		z.bitBuf |= uint32(z.in[z.pos]) << z.bitCnt
		z.pos++
		z.bitCnt += 8
	}
	v := z.bitBuf & (1<<n - 1)
	z.bitBuf >>= n
	z.bitCnt -= n
	return v, nil
}

func (z *inflater) decode(h *huffman) (int, error) {
	code, first, index := 0, 0, 0
	for l := 1; l <= 15; l++ {
		b, err := z.bits(1)
		if err != nil {
			return 0, err
		}
		code |= int(b)
		count := int(h.count[l])
		if code-count < first {
			return int(h.symbol[index+(code-first)]), nil
		}
		index += count
		first += count
		first <<= 1
		code <<= 1
	}
	return 0, errInflate
}

func (z *inflater) alignByte() { z.bitBuf, z.bitCnt = 0, 0 }

// feed appends compressed input and returns newly decompressed bytes. When
// the stream ends, done is true and rest holds the bytes that followed the
// zlib trailer (plain data again). On corruption err is non-nil and the
// inflater must be discarded.
func (z *inflater) feed(p []byte) (out, rest []byte, done bool, err error) {
	if z.pos > 0 {
		z.in = append(z.in[:0], z.in[z.pos:]...)
		z.pos = 0
	}
	z.in = append(z.in, p...)
	if len(z.hist) > 1<<16 {
		z.hist = append(z.hist[:0], z.hist[len(z.hist)-32768:]...)
	}
	start := len(z.hist)
	for {
		stepErr := z.step()
		if stepErr == errNeed {
			break
		}
		if stepErr != nil {
			err = stepErr
			break
		}
		if z.state == izDone {
			done = true
			break
		}
	}
	out = append([]byte(nil), z.hist[start:]...)
	z.adl.write(out)
	if done && err == nil {
		tr := z.in[z.pos : z.pos+4]
		if uint32(tr[0])<<24|uint32(tr[1])<<16|uint32(tr[2])<<8|uint32(tr[3]) != z.adl.sum32() {
			return out, nil, false, errInflate
		}
		z.pos += 4
		rest = append([]byte(nil), z.in[z.pos:]...)
	}
	return out, rest, done, err
}

func (z *inflater) step() error {
	s := z.snap()
	err := z.step1()
	if err == errNeed {
		z.restore(s)
	}
	return err
}

func (z *inflater) step1() error {
	switch z.state {
	case izHeader:
		cmf, err := z.bits(8)
		if err != nil {
			return err
		}
		flg, err := z.bits(8)
		if err != nil {
			return err
		}
		if cmf&0x0f != 8 || cmf>>4 > 7 || (cmf<<8|flg)%31 != 0 || flg&0x20 != 0 {
			return errInflate
		}
		z.state = izBlockHeader
	case izBlockHeader:
		if z.final {
			z.alignByte()
			z.state = izTrailer
			return nil
		}
		v, err := z.bits(3)
		if err != nil {
			return err
		}
		final := v&1 == 1
		switch v >> 1 {
		case 0:
			z.alignByte()
			ln, err := z.bits(16)
			if err != nil {
				return err
			}
			nln, err := z.bits(16)
			if err != nil {
				return err
			}
			if ln != ^nln&0xffff {
				return errInflate
			}
			z.stored = int(ln)
			z.state = izStored
		case 1:
			z.lit, z.dist = fixedTables()
			z.state = izCodes
		case 2:
			if err := z.dynamicTables(); err != nil {
				return err
			}
			z.state = izCodes
		default:
			return errInflate
		}
		z.final = final
	case izStored:
		avail := len(z.in) - z.pos
		if z.stored > 0 && avail == 0 {
			return errNeed
		}
		n := min(avail, z.stored)
		z.hist = append(z.hist, z.in[z.pos:z.pos+n]...)
		z.pos += n
		z.stored -= n
		if z.stored == 0 {
			z.state = izBlockHeader
		}
	case izCodes:
		sym, err := z.decode(z.lit)
		if err != nil {
			return err
		}
		switch {
		case sym < 256:
			z.hist = append(z.hist, byte(sym))
		case sym == 256:
			z.state = izBlockHeader
		default:
			sym -= 257
			if sym >= 29 {
				return errInflate
			}
			eb, err := z.bits(uint(lenExtra[sym]))
			if err != nil {
				return err
			}
			length := int(lenBase[sym]) + int(eb)
			ds, err := z.decode(z.dist)
			if err != nil {
				return err
			}
			if ds >= 30 {
				return errInflate
			}
			db, err := z.bits(uint(dstExtra[ds]))
			if err != nil {
				return err
			}
			d := int(dstBase[ds]) + int(db)
			if d > len(z.hist) {
				return errInflate
			}
			for i := 0; i < length; i++ {
				z.hist = append(z.hist, z.hist[len(z.hist)-d])
			}
		}
	case izTrailer:
		// The adler32 is verified in feed() once the output is folded in.
		if len(z.in)-z.pos < 4 {
			return errNeed
		}
		z.state = izDone
	}
	return nil
}

func (z *inflater) dynamicTables() error {
	hlit, err := z.bits(5)
	if err != nil {
		return err
	}
	hdist, err := z.bits(5)
	if err != nil {
		return err
	}
	hclen, err := z.bits(4)
	if err != nil {
		return err
	}
	nlen, ndist, ncode := int(hlit)+257, int(hdist)+1, int(hclen)+4
	if nlen > 286 || ndist > 30 {
		return errInflate
	}
	var cl [19]uint8
	for i := 0; i < ncode; i++ {
		v, err := z.bits(3)
		if err != nil {
			return err
		}
		cl[clOrder[i]] = uint8(v)
	}
	clh, ok := newHuffman(cl[:])
	if !ok {
		return errInflate
	}
	lengths := make([]uint8, nlen+ndist)
	for i := 0; i < len(lengths); {
		sym, err := z.decode(clh)
		if err != nil {
			return err
		}
		if sym < 16 {
			lengths[i] = uint8(sym)
			i++
			continue
		}
		var prev uint8
		var rep int
		switch sym {
		case 16:
			if i == 0 {
				return errInflate
			}
			prev = lengths[i-1]
			v, err := z.bits(2)
			if err != nil {
				return err
			}
			rep = 3 + int(v)
		case 17:
			v, err := z.bits(3)
			if err != nil {
				return err
			}
			rep = 3 + int(v)
		default:
			v, err := z.bits(7)
			if err != nil {
				return err
			}
			rep = 11 + int(v)
		}
		if i+rep > len(lengths) {
			return errInflate
		}
		for ; rep > 0; rep-- {
			lengths[i] = prev
			i++
		}
	}
	if lengths[256] == 0 {
		return errInflate
	}
	lit, ok := newHuffman(lengths[:nlen])
	if !ok {
		return errInflate
	}
	dist, ok := newHuffman(lengths[nlen:])
	if !ok {
		return errInflate
	}
	z.lit, z.dist = lit, dist
	return nil
}
