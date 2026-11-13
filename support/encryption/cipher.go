// Package encryption provides symmetric encryption for reversible data such as
// configuration, tokens and cookies. Do not use it for passwords (hash those
// with a dedicated slow hash). The implementation matches swoolefy PHP
// Encryption (Laravel-style Encrypter): a ciphertext is base64(JSON{iv, value,
// mac, tag}), CBC additionally carries HMAC-SHA256, GCM relies on the AEAD tag,
// and ciphertexts produced by either language decrypt in the other.
//
// The package depends only on the standard library and keeps no global state;
// callers own the *Encrypter and the key.
// 包 encryption 提供对称加解密，保护配置、Token、Cookie 一类可逆数据，
// 不用于保存密码（密码哈希用专门的慢哈希算法）。实现对齐 swoolefy PHP
// Encryption（Laravel 风格 Encrypter）：密文是 base64(JSON{iv, value, mac, tag})，
// CBC 额外做 HMAC-SHA256，GCM 使用 AEAD tag，两种语言的密文可以互相解密。
//
// 包只依赖标准库，不持有全局状态；调用方自己持有 *Encrypter 和密钥。
package encryption

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Supported cipher names, matched case-insensitively.
// 支持的 cipher 名称，不区分大小写。
const (
	CipherAES128CBC = "aes-128-cbc"
	CipherAES256CBC = "aes-256-cbc"
	CipherAES128GCM = "aes-128-gcm"
	CipherAES256GCM = "aes-256-gcm"
)

// defaultCipher is used when the cipher is empty. PHP defaults to aes-128-cbc;
// the Go side defaults to AES-256-GCM, where AEAD does encryption and integrity
// checking in one step and is the better production default.
// defaultCipher 在 cipher 为空时使用。PHP 版默认 aes-128-cbc；Go 侧默认
// AES-256-GCM：AEAD 一步完成加密与完整性校验，是生产环境的更优默认值。
const defaultCipher = CipherAES256GCM

type cipherInfo struct {
	keySize int  // required key length in bytes; 密钥字节数
	ivSize  int  // IV/nonce 字节数：CBC 为块大小 16，GCM 为标准 nonce 12
	aead    bool // true 表示 GCM，用 tag 校验；false 表示 CBC，用 HMAC-SHA256
}

// supportedCiphers mirrors PHP Encrypter::$supportedCiphers one to one.
// supportedCiphers 与 PHP Encrypter::$supportedCiphers 一一对应。
var supportedCiphers = map[string]cipherInfo{
	CipherAES128CBC: {keySize: 16, ivSize: 16, aead: false},
	CipherAES256CBC: {keySize: 32, ivSize: 16, aead: false},
	CipherAES128GCM: {keySize: 16, ivSize: 12, aead: true},
	CipherAES256GCM: {keySize: 32, ivSize: 12, aead: true},
}

func lookupCipher(cipher string) (cipherInfo, bool) {
	info, ok := supportedCiphers[strings.ToLower(strings.TrimSpace(cipher))]
	return info, ok
}

// Supported reports whether the key and cipher combination is valid: the cipher
// must be in the support table and the key length counted in raw bytes must
// equal the expected size exactly.
// Supported 判断密钥与 cipher 组合是否合法：cipher 必须在支持表内，
// 密钥按原始字节计长度必须正好等于对应值。
func Supported(key []byte, cipher string) bool {
	info, ok := lookupCipher(cipher)
	return ok && len(key) == info.keySize
}

// GenerateKey returns random raw key bytes of the size required by the cipher,
// read from crypto/rand. The result is raw bytes, not hex or base64 text;
// encoding is up to the caller when persisting it.
// GenerateKey 用 crypto/rand 生成指定 cipher 的原始密钥字节。
// 返回的是原始字节而非 hex/base64 文本；持久化时由调用方自行编码。
func GenerateKey(cipher string) ([]byte, error) {
	info, ok := lookupCipher(cipher)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCipher, cipher)
	}
	key := make([]byte, info.keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("encryption: generate key: %w", err)
	}
	return key, nil
}
