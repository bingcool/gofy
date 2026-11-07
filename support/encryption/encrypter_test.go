package encryption_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bingcool/gofy/support/encryption"
)

var (
	key16 = []byte("0123456789abcdef")
	key32 = []byte("0123456789abcdef0123456789abcdef")

	ciphers = []struct {
		name string
		key  []byte
	}{
		{encryption.CipherAES128CBC, key16},
		{encryption.CipherAES256CBC, key32},
		{encryption.CipherAES128GCM, key16},
		{encryption.CipherAES256GCM, key32},
	}
)

func newEncrypter(t *testing.T, key []byte, cipher string) *encryption.Encrypter {
	t.Helper()
	e, err := encryption.NewEncrypter(key, cipher)
	if err != nil {
		t.Fatalf("NewEncrypter(%s): %v", cipher, err)
	}
	return e
}

func TestNewEncrypterRejectsInvalid(t *testing.T) {
	cases := []struct {
		key    []byte
		cipher string
	}{
		{key16, encryption.CipherAES256CBC},
		{key32, encryption.CipherAES128CBC},
		{key16, encryption.CipherAES256GCM},
		{key32, encryption.CipherAES128GCM},
		{nil, encryption.CipherAES128CBC},
		{key16, "aes-192-cbc"},
		{key16, "des-cbc"},
	}
	for _, tc := range cases {
		if _, err := encryption.NewEncrypter(tc.key, tc.cipher); !errors.Is(err, encryption.ErrUnsupportedCipher) {
			t.Fatalf("NewEncrypter(%d bytes, %s) = %v, want ErrUnsupportedCipher", len(tc.key), tc.cipher, err)
		}
	}
}

func TestNewEncrypterDefaultAndCase(t *testing.T) {
	e := newEncrypter(t, key32, "")
	if e.Cipher() != encryption.CipherAES256GCM {
		t.Fatalf("default cipher = %s, want aes-256-gcm", e.Cipher())
	}
	e = newEncrypter(t, key16, " AES-128-CBC ")
	if e.Cipher() != encryption.CipherAES128CBC {
		t.Fatalf("cipher = %s, want normalized aes-128-cbc", e.Cipher())
	}
}

func TestSupported(t *testing.T) {
	for _, c := range ciphers {
		if !encryption.Supported(c.key, c.name) {
			t.Fatalf("Supported(%s) = false", c.name)
		}
		if !encryption.Supported(c.key, strings.ToUpper(c.name)) {
			t.Fatalf("Supported is not case-insensitive for %s", c.name)
		}
		if encryption.Supported(c.key[:len(c.key)-1], c.name) {
			t.Fatalf("Supported accepted a short key for %s", c.name)
		}
	}
	if encryption.Supported(key16, "aes-192-cbc") {
		t.Fatal("Supported accepted an unknown cipher")
	}
}

func TestGenerateKey(t *testing.T) {
	for _, c := range ciphers {
		key, err := encryption.GenerateKey(c.name)
		if err != nil {
			t.Fatalf("GenerateKey(%s): %v", c.name, err)
		}
		if len(key) != len(c.key) {
			t.Fatalf("GenerateKey(%s) length = %d, want %d", c.name, len(key), len(c.key))
		}
		if !encryption.Supported(key, c.name) {
			t.Fatalf("generated key is not supported by %s", c.name)
		}
		again, err := encryption.GenerateKey(c.name)
		if err != nil {
			t.Fatalf("GenerateKey again: %v", err)
		}
		if string(key) == string(again) {
			t.Fatalf("GenerateKey(%s) returned the same key twice", c.name)
		}
	}
	if _, err := encryption.GenerateKey("aes-192-cbc"); !errors.Is(err, encryption.ErrUnsupportedCipher) {
		t.Fatalf("GenerateKey(unknown) = %v, want ErrUnsupportedCipher", err)
	}
}

type secretValue struct {
	UserID int    `json:"user_id"`
	Token  string `json:"token"`
}

func TestRoundTrip(t *testing.T) {
	for _, c := range ciphers {
		t.Run(c.name, func(t *testing.T) {
			e := newEncrypter(t, c.key, c.name)

			want := secretValue{UserID: 42, Token: "令牌-π"}
			payload, err := e.Encrypt(want)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			var got secretValue
			if err := e.Decrypt(payload, &got); err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if got != want {
				t.Fatalf("Decrypt = %+v, want %+v", got, want)
			}

			for _, text := range []string{"", "secret", "hello gofy 加密互通", strings.Repeat("x", 4096)} {
				payload, err := e.EncryptString(text)
				if err != nil {
					t.Fatalf("EncryptString: %v", err)
				}
				gotText, err := e.DecryptString(payload)
				if err != nil {
					t.Fatalf("DecryptString: %v", err)
				}
				if gotText != text {
					t.Fatalf("DecryptString = %q, want %q", gotText, text)
				}
			}
		})
	}
}

func TestEncryptUsesFreshIV(t *testing.T) {
	for _, c := range ciphers {
		e := newEncrypter(t, c.key, c.name)
		first, err := e.EncryptString("same plaintext")
		if err != nil {
			t.Fatalf("EncryptString: %v", err)
		}
		second, err := e.EncryptString("same plaintext")
		if err != nil {
			t.Fatalf("EncryptString: %v", err)
		}
		if first == second {
			t.Fatalf("%s reused the IV for identical plaintext", c.name)
		}
	}
}

func TestDecryptWrongKey(t *testing.T) {
	for _, c := range ciphers {
		e := newEncrypter(t, c.key, c.name)
		payload, err := e.EncryptString("secret")
		if err != nil {
			t.Fatalf("EncryptString: %v", err)
		}
		other := append([]byte(nil), c.key...)
		other[0] ^= 0xff
		wrong := newEncrypter(t, other, c.name)
		if _, err := wrong.DecryptString(payload); err == nil {
			t.Fatalf("%s decrypted with a wrong key", c.name)
		}
	}
}

// mutatePayload 解码载荷、执行修改后重新编码，用于构造篡改场景。
func mutatePayload(t *testing.T, payload string, fn func(map[string]string)) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(decoded, &fields); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	fn(fields)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

func flipBase64Byte(t *testing.T, value string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode field: %v", err)
	}
	raw[0] ^= 0xff
	return base64.StdEncoding.EncodeToString(raw)
}

func TestTamperedPayload(t *testing.T) {
	for _, c := range ciphers {
		t.Run(c.name, func(t *testing.T) {
			e := newEncrypter(t, c.key, c.name)
			payload, err := e.EncryptString("secret")
			if err != nil {
				t.Fatalf("EncryptString: %v", err)
			}
			isGCM := strings.Contains(c.name, "gcm")

			tamperedValue := mutatePayload(t, payload, func(f map[string]string) {
				f["value"] = flipBase64Byte(t, f["value"])
			})
			_, err = e.DecryptString(tamperedValue)
			if isGCM && !errors.Is(err, encryption.ErrDecrypt) {
				t.Fatalf("tampered gcm value = %v, want ErrDecrypt", err)
			}
			if !isGCM && !errors.Is(err, encryption.ErrMAC) {
				t.Fatalf("tampered cbc value = %v, want ErrMAC", err)
			}

			tamperedIV := mutatePayload(t, payload, func(f map[string]string) {
				f["iv"] = flipBase64Byte(t, f["iv"])
			})
			_, err = e.DecryptString(tamperedIV)
			if isGCM && !errors.Is(err, encryption.ErrDecrypt) {
				t.Fatalf("tampered gcm iv = %v, want ErrDecrypt", err)
			}
			if !isGCM && !errors.Is(err, encryption.ErrMAC) {
				t.Fatalf("tampered cbc iv = %v, want ErrMAC", err)
			}

			if !isGCM {
				badMAC := mutatePayload(t, payload, func(f map[string]string) {
					f["mac"] = strings.Repeat("0", 64)
				})
				if _, err := e.DecryptString(badMAC); !errors.Is(err, encryption.ErrMAC) {
					t.Fatalf("bad mac = %v, want ErrMAC", err)
				}
				withTag := mutatePayload(t, payload, func(f map[string]string) {
					f["tag"] = base64.StdEncoding.EncodeToString(make([]byte, 16))
				})
				if _, err := e.DecryptString(withTag); !errors.Is(err, encryption.ErrTag) {
					t.Fatalf("cbc payload with tag = %v, want ErrTag", err)
				}
			} else {
				shortTag := mutatePayload(t, payload, func(f map[string]string) {
					f["tag"] = base64.StdEncoding.EncodeToString(make([]byte, 8))
				})
				if _, err := e.DecryptString(shortTag); !errors.Is(err, encryption.ErrTag) {
					t.Fatalf("short gcm tag = %v, want ErrTag", err)
				}
				badTag := mutatePayload(t, payload, func(f map[string]string) {
					f["tag"] = flipBase64Byte(t, f["tag"])
				})
				if _, err := e.DecryptString(badTag); !errors.Is(err, encryption.ErrDecrypt) {
					t.Fatalf("tampered gcm tag = %v, want ErrDecrypt", err)
				}
			}
		})
	}
}

func TestInvalidPayload(t *testing.T) {
	e := newEncrypter(t, key32, encryption.CipherAES256GCM)
	cases := []string{
		"",
		"not-base64!!",
		base64.StdEncoding.EncodeToString([]byte("not json")),
		base64.StdEncoding.EncodeToString([]byte(`{"iv":"x","value":"y"}`)),
		base64.StdEncoding.EncodeToString([]byte(`{"iv":"","value":"eA==","mac":"","tag":""}`)),
		base64.StdEncoding.EncodeToString([]byte(`{"iv":"eA==","value":"eA==","mac":"","tag":""}`)),
	}
	for _, payload := range cases {
		if _, err := e.DecryptString(payload); !errors.Is(err, encryption.ErrPayload) && !errors.Is(err, encryption.ErrTag) {
			t.Fatalf("DecryptString(%q) = %v, want ErrPayload or ErrTag", payload, err)
		}
	}
}

func TestDecryptNilDestination(t *testing.T) {
	e := newEncrypter(t, key32, "")
	payload, err := e.EncryptString("secret")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	if err := e.Decrypt(payload, nil); !errors.Is(err, encryption.ErrDecrypt) {
		t.Fatalf("Decrypt(nil) = %v, want ErrDecrypt", err)
	}
}

func TestEncryptRejectsUnserializable(t *testing.T) {
	e := newEncrypter(t, key32, "")
	if _, err := e.Encrypt(make(chan int)); !errors.Is(err, encryption.ErrEncrypt) {
		t.Fatalf("Encrypt(chan) = %v, want ErrEncrypt", err)
	}
}

func TestKeyIsCopied(t *testing.T) {
	key := append([]byte(nil), key32...)
	e := newEncrypter(t, key, "")
	for i := range key {
		key[i] = 0
	}
	payload, err := e.EncryptString("secret")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	if got, err := e.DecryptString(payload); err != nil || got != "secret" {
		t.Fatalf("mutating the caller key affected the encrypter: %q, %v", got, err)
	}
}

// PHP 互通固定密文：由 swoolefy Encrypter::encryptString 生成，
// 明文统一为 "hello gofy 加密互通"，密钥即上面的 key16/key32。
// 重新生成方式见 support/encryption/README.md 的测试一节。
var phpFixtures = []struct {
	cipher  string
	key     []byte
	payload string
}{
	{encryption.CipherAES128CBC, key16, "eyJpdiI6IlNpMVdmYndQZlRGL2NRS2xMNUQzR2c9PSIsInZhbHVlIjoiRWI2MHpySk5pSDE4dFFJSVhHbGFIQVo5UWZPaTJwb3Roak9NQWNaYUhEYz0iLCJtYWMiOiIxMmIzZDFhZGU1M2FlMTM4NTkzYmQ3MTI3OWFiMTQyMTJkZDIxYmRjY2U0ZWYzOWNmMDczY2VlZjhhZTk0MDI0IiwidGFnIjoiIn0="},
	{encryption.CipherAES256CBC, key32, "eyJpdiI6IlpQNkNQWFM5eFUwb3VUNkgyUDlpL2c9PSIsInZhbHVlIjoiVkNTaUQvcFNGTEZIVnFKQi9CMXAreFkvSXN2S1Jpb1JOUE9uTW4yZkJrQT0iLCJtYWMiOiI5NmY3NDU3YjVlNDU5NGJkMjQ2ZGMxY2U3ZGRkNTRmNWViNTJhMjE5YzgxZDFiMzg1OWIxNTNiNDVhMmYyM2NiIiwidGFnIjoiIn0="},
	{encryption.CipherAES128GCM, key16, "eyJpdiI6IjlocFhOSmhkL0VXL2NBSWciLCJ2YWx1ZSI6IlMwZmE2Um5wcS9tVFVFZ1VHSU9hUkhmU0Y3MmlPQVU9IiwibWFjIjoiIiwidGFnIjoiVXB1eTQreGJMUXhPVGNGOXE5akg4dz09In0="},
	{encryption.CipherAES256GCM, key32, "eyJpdiI6IjQrSlJ0NjdCZkpYaWM2U2MiLCJ2YWx1ZSI6IjFFcEUybXNtWC9QdWk4b09sNzhUSFdxUEhKbGRzL1U9IiwibWFjIjoiIiwidGFnIjoiVkxvSTZwZUMya0xPQnY3TXF0TGZYZz09In0="},
}

func TestDecryptPHPFixtures(t *testing.T) {
	for _, f := range phpFixtures {
		t.Run(f.cipher, func(t *testing.T) {
			e := newEncrypter(t, f.key, f.cipher)
			got, err := e.DecryptString(f.payload)
			if err != nil {
				t.Fatalf("DecryptString(php payload): %v", err)
			}
			if want := "hello gofy 加密互通"; got != want {
				t.Fatalf("DecryptString = %q, want %q", got, want)
			}
		})
	}
}
