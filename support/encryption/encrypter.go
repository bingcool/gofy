package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrUnsupportedCipher 表示 cipher 不在支持表内，或密钥字节数不匹配。
	ErrUnsupportedCipher = errors.New("encryption: unsupported cipher or incorrect key length")
	// ErrEncrypt 表示加密过程失败（序列化、随机数或底层 cipher 错误）。
	ErrEncrypt = errors.New("encryption: could not encrypt the data")
	// ErrDecrypt 表示解密失败：密钥或 cipher 与加密时不一致，或数据损坏。
	ErrDecrypt = errors.New("encryption: could not decrypt the data")
	// ErrPayload 表示载荷不是合法 base64/JSON、缺字段或 IV 长度不对。
	ErrPayload = errors.New("encryption: the payload is invalid")
	// ErrMAC 表示 CBC 载荷的 HMAC-SHA256 校验失败。
	ErrMAC = errors.New("encryption: the MAC is invalid")
	// ErrTag 表示 GCM 载荷的 tag 缺失或长度不是 16 字节。
	ErrTag = errors.New("encryption: the tag is invalid")
)

// gcmTagSize 与 PHP openssl 的 GCM tag 长度一致。
const gcmTagSize = 16

// payload 是密文的 JSON 结构，字段与 PHP Encrypter 完全一致：
// iv/tag 是 base64，value 是 base64 密文，mac 是 hex HMAC（GCM 为空）。
type payload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	MAC   string `json:"mac"`
	Tag   string `json:"tag"`
}

// Encrypter 是对称加解密器。创建后不可变，可并发使用。
// 密钥在构造时复制，调用方修改原切片不影响实例；本类型不提供读取密钥的方法，
// 避免密钥被意外写入日志。
type Encrypter struct {
	key    []byte
	cipher string
	info   cipherInfo
	block  cipher.Block // CBC 使用
	aead   cipher.AEAD  // GCM 使用
}

// NewEncrypter 校验密钥与 cipher 并创建加解密器。cipher 不区分大小写，
// 空字符串使用默认 aes-256-gcm。密钥是原始字节：aes-128-* 需要 16 字节，
// aes-256-* 需要 32 字节；hex/base64 文本请先解码再传入。
func NewEncrypter(key []byte, cipherName string) (*Encrypter, error) {
	if strings.TrimSpace(cipherName) == "" {
		cipherName = defaultCipher
	}
	info, ok := lookupCipher(cipherName)
	if !ok || len(key) != info.keySize {
		return nil, fmt.Errorf("%w: %q (want %s)", ErrUnsupportedCipher, cipherName,
			strings.Join([]string{CipherAES128CBC, CipherAES256CBC, CipherAES128GCM, CipherAES256GCM}, ", "))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encryption: %w", err)
	}
	e := &Encrypter{
		key:    append([]byte(nil), key...),
		cipher: strings.ToLower(strings.TrimSpace(cipherName)),
		info:   info,
		block:  block,
	}
	if info.aead {
		e.aead, err = cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("encryption: %w", err)
		}
	}
	return e, nil
}

// Cipher 返回规范化后的 cipher 名称。
func (e *Encrypter) Cipher() string { return e.cipher }

// Encrypt 把 value 序列化为 JSON 后加密，返回 base64 载荷。
// 解密用 Decrypt 并提供对应的接收类型。注意序列化格式是 JSON，与 PHP 的
// serialize 不同：跨语言互通请使用 EncryptString/DecryptString。
func (e *Encrypter) Encrypt(value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEncrypt, err)
	}
	return e.encryptRaw(plain)
}

// Decrypt 校验并解密载荷，再把明文 JSON 反序列化到 out（必须是非 nil 指针）。
// 完整性校验（CBC 的 MAC / GCM 的 tag）通过之前不会解析明文。
func (e *Encrypter) Decrypt(payloadText string, out any) error {
	if out == nil {
		return fmt.Errorf("%w: destination must not be nil", ErrDecrypt)
	}
	plain, err := e.decryptRaw(payloadText)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return nil
}

// EncryptString 按原始字符串加密，不做序列化，与 PHP encryptString 互通。
func (e *Encrypter) EncryptString(value string) (string, error) {
	return e.encryptRaw([]byte(value))
}

// DecryptString 校验并解密载荷，返回原始明文，与 PHP decryptString 互通。
func (e *Encrypter) DecryptString(payloadText string) (string, error) {
	plain, err := e.decryptRaw(payloadText)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (e *Encrypter) encryptRaw(plain []byte) (string, error) {
	iv := make([]byte, e.info.ivSize)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("%w: %v", ErrEncrypt, err)
	}

	var ciphertext, tag []byte
	if e.info.aead {
		// GCM Seal 返回 密文||tag，拆开后分别 base64，与 PHP 载荷字段对齐。
		sealed := e.aead.Seal(nil, iv, plain, nil)
		ciphertext = sealed[:len(sealed)-gcmTagSize]
		tag = sealed[len(sealed)-gcmTagSize:]
	} else {
		padded := padPKCS7(plain, e.block.BlockSize())
		ciphertext = make([]byte, len(padded))
		cipher.NewCBCEncrypter(e.block, iv).CryptBlocks(ciphertext, padded)
	}

	p := payload{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:   base64.StdEncoding.EncodeToString(tag),
	}
	if !e.info.aead {
		// PHP 的 MAC 输入是 base64(iv) 与 base64(value) 两个字符串的拼接，
		// 输出 hex；保持逐字节一致才能跨语言校验。
		p.MAC = e.mac(p.IV, p.Value)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEncrypt, err)
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func (e *Encrypter) decryptRaw(payloadText string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(payloadText)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayload, err)
	}
	// 用 map 解码以检查字段存在性，对齐 PHP 的 isset(iv, value, mac)。
	var fields map[string]string
	if err := json.Unmarshal(decoded, &fields); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayload, err)
	}
	for _, key := range []string{"iv", "value", "mac"} {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("%w: missing %q", ErrPayload, key)
		}
	}
	iv, err := base64.StdEncoding.DecodeString(fields["iv"])
	if err != nil || len(iv) != e.info.ivSize {
		return nil, fmt.Errorf("%w: iv length must be %d bytes", ErrPayload, e.info.ivSize)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(fields["value"])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayload, err)
	}

	if e.info.aead {
		tag, err := base64.StdEncoding.DecodeString(fields["tag"])
		if err != nil || len(tag) != gcmTagSize {
			return nil, fmt.Errorf("%w: gcm tag must be %d bytes", ErrTag, gcmTagSize)
		}
		plain, err := e.aead.Open(nil, iv, append(ciphertext, tag...), nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
		}
		return plain, nil
	}

	// CBC 先校验 MAC 再解密；载荷带 tag 说明算法被混淆，直接拒绝。
	if fields["tag"] != "" {
		return nil, fmt.Errorf("%w: cipher %q does not support AEAD", ErrTag, e.cipher)
	}
	if !hmac.Equal([]byte(e.mac(fields["iv"], fields["value"])), []byte(fields["mac"])) {
		return nil, ErrMAC
	}
	if len(ciphertext) == 0 || len(ciphertext)%e.block.BlockSize() != 0 {
		return nil, fmt.Errorf("%w: ciphertext is not block aligned", ErrDecrypt)
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(e.block, iv).CryptBlocks(plain, ciphertext)
	plain, err = unpadPKCS7(plain, e.block.BlockSize())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return plain, nil
}

func (e *Encrypter) mac(iv, value string) string {
	m := hmac.New(sha256.New, e.key)
	m.Write([]byte(iv + value))
	return hex.EncodeToString(m.Sum(nil))
}

// padPKCS7 按块大小补齐；明文正好对齐时补一整块，与 openssl 默认填充一致。
func padPKCS7(plain []byte, blockSize int) []byte {
	n := blockSize - len(plain)%blockSize
	out := make([]byte, len(plain)+n)
	copy(out, plain)
	for i := len(plain); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func unpadPKCS7(plain []byte, blockSize int) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(plain[len(plain)-1])
	if n == 0 || n > blockSize || n > len(plain) {
		return nil, errors.New("invalid padding")
	}
	for _, b := range plain[len(plain)-n:] {
		if int(b) != n {
			return nil, errors.New("invalid padding")
		}
	}
	return plain[:len(plain)-n], nil
}
