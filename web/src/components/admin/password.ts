// Random passwords for new local accounts and password resets.

/** No look-alike characters (0/O, 1/l/I) so a password can be read out or retyped. */
const LOWER = 'abcdefghijkmnpqrstuvwxyz'
const UPPER = 'ABCDEFGHJKLMNPQRSTUVWXYZ'
const DIGITS = '23456789'
const ALPHABET = LOWER + UPPER + DIGITS

/** Fills the buffer with random values (crypto.getRandomValues; injectable for tests). */
export type RandomFill = (buf: Uint32Array<ArrayBuffer>) => void

/** Returns a uniformly random integer in [0, max) from a CSPRNG (rejection sampling, no modulo bias). */
function randomIndex(max: number, random: RandomFill): number {
  const limit = Math.floor(0x1_0000_0000 / max) * max
  const buf = new Uint32Array(new ArrayBuffer(4))
  for (;;) {
    random(buf)
    if (buf[0] < limit) return buf[0] % max
  }
}

const cryptoRandom: RandomFill = (buf) => {
  crypto.getRandomValues(buf)
}

/**
 * Generates a readable random password like "xK7p-m2Qa-9Rtw": `groups` blocks
 * of 4 characters joined by dashes, with at least one lower-case letter, one
 * capital and one digit. 12 random characters from 56 symbols ≈ 70 bits.
 */
export function generatePassword(groups = 3, random: RandomFill = cryptoRandom): string {
  const length = Math.max(2, groups) * 4
  for (;;) {
    let chars = ''
    for (let i = 0; i < length; i++) chars += ALPHABET[randomIndex(ALPHABET.length, random)]
    const strong = /[a-z]/.test(chars) && /[A-Z]/.test(chars) && /\d/.test(chars)
    if (!strong) continue
    return chars.match(/.{4}/g)!.join('-')
  }
}
