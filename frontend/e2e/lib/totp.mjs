// RFC 6238 TOTP (HMAC-SHA1 by default) using only node:crypto. Secret is Base32 (RFC 4648), spaces/case ignored.
import crypto from 'node:crypto'

export function base32Decode(s) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  const clean = s.replace(/[\s=-]/g, '').toUpperCase()
  let bits = 0, value = 0
  const out = []
  for (const ch of clean) {
    const i = alphabet.indexOf(ch)
    if (i < 0) throw new Error('invalid base32 character')
    value = (value << 5) | i
    bits += 5
    if (bits >= 8) { out.push((value >>> (bits - 8)) & 0xff); bits -= 8 }
  }
  return Buffer.from(out)
}

export function hotp(key, counter, digits = 6, algo = 'sha1') {
  const buf = Buffer.alloc(8)
  buf.writeBigUInt64BE(BigInt(counter))
  const h = crypto.createHmac(algo, key).update(buf).digest()
  const off = h[h.length - 1] & 0xf
  const bin = ((h[off] & 0x7f) << 24) | (h[off + 1] << 16) | (h[off + 2] << 8) | h[off + 3]
  return String(bin % 10 ** digits).padStart(digits, '0')
}

/** `at` is a ms timestamp (default now). `stepOffset` lets callers get the next window's code (replay protection forbids reusing a step). */
export function totp(secretB32, { at = Date.now(), period = 30, digits = 6, algo = 'sha1', stepOffset = 0 } = {}) {
  return hotp(base32Decode(secretB32), Math.floor(at / 1000 / period) + stepOffset, digits, algo)
}

// RFC 6238 appendix B check (SHA1, secret "12345678901234567890", 8 digits): T=59 -> 94287082
export function selfTest() {
  const key = Buffer.from('12345678901234567890')
  return hotp(key, Math.floor(59 / 30), 8) === '94287082'
}
