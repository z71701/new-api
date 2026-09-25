#!/usr/bin/env node
/**
 * Desktop auth password crypto interop helper.
 *
 * Implements the v2 password envelope used by the new-api desktop client:
 *   v2.<base64(wrappedKey)>.<base64(nonce)>.<base64(ciphertext)>
 * where
 *   - wrappedKey  = RSA-OAEP(SHA-256, label="password-v2") of the 32-byte AES key
 *   - nonce       = 12-byte GCM nonce
 *   - ciphertext  = AES-256-GCM(plaintext=password, AAD="password-v2:"+keyID) || GCM tag
 *
 * Also supports the legacy (non-v2) envelope:
 *   <base64(RSA-OAEP(SHA-256, label=nil) of the password)>
 *
 * Pure Node.js built-in `crypto` module; no npm dependencies.
 */
'use strict';

const crypto = require('crypto');
const fs = require('fs');

const OAEP_LABEL = Buffer.from('password-v2', 'utf8');
const V2_PREFIX = 'v2.';

/**
 * Encrypt a password into the v2 envelope.
 *
 * @param {string|Buffer} publicKeyPEM - PEM-encoded SPKI public key.
 * @param {string} keyID - 32-char hex key identifier (sha256(SPKI DER)[:16]).
 * @param {string} password - plaintext password (UTF-8).
 * @param {Buffer} aesKey - 32-byte AES-256 key.
 * @param {Buffer} nonce - 12-byte GCM nonce.
 * @returns {string} v2 envelope string.
 */
function encryptPassword(publicKeyPEM, keyID, password, aesKey, nonce) {
  if (!Buffer.isBuffer(aesKey) || aesKey.length !== 32) {
    throw new Error('aesKey must be a 32-byte Buffer');
  }
  if (!Buffer.isBuffer(nonce) || nonce.length !== 12) {
    throw new Error('nonce must be a 12-byte Buffer');
  }
  const wrappedKey = crypto.publicEncrypt(
    {
      key: publicKeyPEM,
      oaepHash: 'sha256',
      oaepLabel: OAEP_LABEL,
      padding: crypto.constants.RSA_PKCS1_OAEP_PADDING,
    },
    aesKey,
  );

  const aad = Buffer.from('password-v2:' + keyID, 'utf8');
  const cipher = crypto.createCipheriv('aes-256-gcm', aesKey, nonce);
  cipher.setAAD(aad);
  const pt = Buffer.from(password, 'utf8');
  const ct = Buffer.concat([cipher.update(pt), cipher.final()]);
  const tag = cipher.getAuthTag();
  const ciphertext = Buffer.concat([ct, tag]);

  return (
    V2_PREFIX +
    wrappedKey.toString('base64') +
    '.' +
    nonce.toString('base64') +
    '.' +
    ciphertext.toString('base64')
  );
}

/**
 * Decrypt a v2 (or legacy) envelope.
 *
 * @param {string|Buffer} privateKeyPEM - PEM-encoded PKCS#8 private key.
 * @param {string} keyID - 32-char hex key identifier; must match the key used to seal.
 * @param {string} envelope - v2 envelope or legacy base64 ciphertext.
 * @returns {string} plaintext password.
 */
function decryptPassword(privateKeyPEM, keyID, envelope) {
  if (typeof envelope !== 'string') {
    throw new Error('envelope must be a string');
  }
  if (envelope.startsWith(V2_PREFIX)) {
    const rest = envelope.slice(V2_PREFIX.length);
    const parts = rest.split('.');
    if (parts.length !== 3) {
      throw new Error('invalid v2 envelope: expected 4 dot-separated segments');
    }
    const wrappedKey = Buffer.from(parts[0], 'base64');
    const nonce = Buffer.from(parts[1], 'base64');
    const ciphertext = Buffer.from(parts[2], 'base64');
    if (nonce.length !== 12) {
      throw new Error('invalid v2 envelope: nonce must be 12 bytes');
    }
    if (ciphertext.length <= 16) {
      throw new Error('invalid v2 envelope: ciphertext too short');
    }
    const aesKey = crypto.privateDecrypt(
      {
        key: privateKeyPEM,
        oaepHash: 'sha256',
        oaepLabel: OAEP_LABEL,
        padding: crypto.constants.RSA_PKCS1_OAEP_PADDING,
      },
      wrappedKey,
    );
    if (aesKey.length !== 32) {
      throw new Error('invalid v2 envelope: unwrapped AES key must be 32 bytes');
    }
    const tag = ciphertext.subarray(ciphertext.length - 16);
    const ct = ciphertext.subarray(0, ciphertext.length - 16);
    const decipher = crypto.createDecipheriv('aes-256-gcm', aesKey, nonce);
    decipher.setAuthTag(tag);
    decipher.setAAD(Buffer.from('password-v2:' + keyID, 'utf8'));
    const pt = Buffer.concat([decipher.update(ct), decipher.final()]);
    return pt.toString('utf8');
  }

  // Legacy: direct RSA-OAEP(SHA-256, label=nil) over the password.
  const ciphertext = Buffer.from(envelope, 'base64');
  const plaintext = crypto.privateDecrypt(
    {
      key: privateKeyPEM,
      oaepHash: 'sha256',
      padding: crypto.constants.RSA_PKCS1_OAEP_PADDING,
    },
    ciphertext,
  );
  return plaintext.toString('utf8');
}

/**
 * Encrypt with the legacy format (RSA-OAEP/SHA-256, no label). Exposed for
 * interop testing of the legacy code path.
 */
function encryptPasswordLegacy(publicKeyPEM, password) {
  return crypto
    .publicEncrypt(
      {
        key: publicKeyPEM,
        oaepHash: 'sha256',
        padding: crypto.constants.RSA_PKCS1_OAEP_PADDING,
      },
      Buffer.from(password, 'utf8'),
    )
    .toString('base64');
}

// ---- CLI -------------------------------------------------------------------

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith('--')) {
      const name = a.slice(2);
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('--')) {
        out[name] = true;
      } else {
        out[name] = next;
        i++;
      }
    } else {
      out._cmd = a;
    }
  }
  return out;
}

function usage() {
  process.stderr.write(
    [
      'Usage:',
      '  node desktop-auth-crypto-interop.js encrypt --public-key <pem> --key-id <hex> --password <text> --aes-key <hex> --nonce <hex>',
      '  node desktop-auth-crypto-interop.js decrypt --private-key <pem> --key-id <hex> --ciphertext <text>',
      '  node desktop-auth-crypto-interop.js encrypt-legacy --public-key <pem> --password <text>',
      '  node desktop-auth-crypto-interop.js selftest',
      '',
    ].join('\n'),
  );
  process.exit(2);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const cmd = args._cmd;

  if (cmd === 'encrypt') {
    if (!args['public-key'] || !args['key-id'] || args.password === undefined || !args['aes-key'] || !args.nonce) {
      usage();
    }
    const pub = fs.readFileSync(args['public-key'], 'utf8');
    const aesKey = Buffer.from(args['aes-key'], 'hex');
    const nonce = Buffer.from(args.nonce, 'hex');
    const out = encryptPassword(pub, args['key-id'], args.password, aesKey, nonce);
    process.stdout.write(out + '\n');
    return;
  }

  if (cmd === 'decrypt') {
    if (!args['private-key'] || !args['key-id'] || !args.ciphertext) {
      usage();
    }
    const priv = fs.readFileSync(args['private-key'], 'utf8');
    const out = decryptPassword(priv, args['key-id'], args.ciphertext);
    process.stdout.write(out + '\n');
    return;
  }

  if (cmd === 'encrypt-legacy') {
    if (!args['public-key'] || args.password === undefined) {
      usage();
    }
    const pub = fs.readFileSync(args['public-key'], 'utf8');
    const out = encryptPasswordLegacy(pub, args.password);
    process.stdout.write(out + '\n');
    return;
  }

  if (cmd === 'selftest') {
    const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', {
      modulusLength: 2048,
      publicKeyEncoding: { type: 'spki', format: 'pem' },
      privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
    });
    const pubDER = crypto
      .createPublicKey(publicKey)
      .export({ type: 'spki', format: 'der' });
    const keyID = crypto.createHash('sha256').update(pubDER).digest().subarray(0, 16).toString('hex');

    const vectors = ['p@ssw0rd-中文-🔐', 'a', 'long-password-'.repeat(20)];
    for (const v of vectors) {
      const aesKey = crypto.randomBytes(32);
      const nonce = crypto.randomBytes(12);
      const envelope = encryptPassword(publicKey, keyID, v, aesKey, nonce);
      const back = decryptPassword(privateKey, keyID, envelope);
      if (back !== v) {
        console.error('selftest mismatch for', JSON.stringify(v));
        process.exit(1);
      }
      // legacy round-trip
      const leg = encryptPasswordLegacy(publicKey, v.slice(0, 100));
      const legBack = decryptPassword(privateKey, keyID, leg);
      if (legBack !== v.slice(0, 100)) {
        console.error('selftest legacy mismatch');
        process.exit(1);
      }
    }
    process.stdout.write('SELFTEST OK\n');
    return;
  }

  usage();
}

main();

module.exports = { encryptPassword, decryptPassword, encryptPasswordLegacy, OAEP_LABEL, V2_PREFIX };
