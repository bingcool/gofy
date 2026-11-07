# support/encryption

对称加解密，保护配置、Token、Cookie 一类可逆数据，**不用于保存密码**（密码哈希
应使用 bcrypt/argon2 等慢哈希）。实现对齐 swoolefy PHP `Encryption`
（Laravel 风格 Encrypter）：CBC 做 HMAC-SHA256，GCM 使用 AEAD tag，密文格式与
PHP 版逐字节兼容，两种语言可以互相解密。

包只依赖标准库，不持有全局状态；`*Encrypter` 创建后不可变，可并发使用。

## 快速开始

```go
import (
    "encoding/base64"
    "fmt"
    "os"

    "github.com/bingcool/gofy/support/encryption"
)

// 生成一次并持久化到环境变量（原始字节，这里用 base64 编码保存）
key, err := encryption.GenerateKey(encryption.CipherAES256GCM)
if err != nil {
    return err
}
fmt.Println(base64.StdEncoding.EncodeToString(key))

// 运行时从环境变量解码出原始字节
key, err = base64.StdEncoding.DecodeString(os.Getenv("APP_ENCRYPTION_KEY"))
if err != nil {
    return err
}
encrypter, err := encryption.NewEncrypter(key, encryption.CipherAES256GCM)
if err != nil {
    return err
}

payload, err := encrypter.EncryptString("secret")
plain, err := encrypter.DecryptString(payload)
```

加密和解密必须使用同一把密钥、同一种 cipher。密钥是**原始字节**，不是 hex 或
base64 文本本身；16 个 ASCII 字符只满足 `aes-128-*`。

## 支持的算法

cipher 名称不区分大小写，密钥按字节计长度必须正好等于下表：

| cipher | 密钥长度 | IV/nonce | 完整性 |
| --- | --- | --- | --- |
| `aes-128-cbc` | 16 字节 | 16 字节 | HMAC-SHA256 |
| `aes-256-cbc` | 32 字节 | 16 字节 | HMAC-SHA256 |
| `aes-128-gcm` | 16 字节 | 12 字节 | GCM tag（16 字节） |
| `aes-256-gcm` | 32 字节 | 12 字节 | GCM tag（16 字节） |

`NewEncrypter` 的 cipher 参数为空时默认 `aes-256-gcm`。这是与 PHP 版唯一的
行为差异：PHP 默认 `aes-128-cbc`，Go 侧选择 AEAD 作为更安全的生产默认值；
需要 CBC 时显式传入即可。

`Supported(key, cipher)` 预检组合是否合法；`GenerateKey(cipher)` 用
`crypto/rand` 生成对应长度的原始密钥，未知 cipher 返回错误。

## 两个入口的区别

| 方法 | 明文 | 解密 |
| --- | --- | --- |
| `Encrypt(value any)` | 先 `json.Marshal`，可传结构体、map、切片 | `Decrypt(payload, &out)` |
| `EncryptString(string)` | 按原始字符串加密，不序列化 | `DecryptString(payload)` |

两边不能交叉调用：`Encrypt` 的结果用 `DecryptString` 解开得到的是 JSON 文本。

**跨语言注意**：PHP `encrypt()` 使用 PHP `serialize`，Go `Encrypt()` 使用
JSON，值级入口不能跨语言互通；`EncryptString`/`DecryptString` 两边格式一致，
可以互解（已有四种 cipher 的双向互通测试）。

## 密文格式

返回值是一段 base64 字符串，解码后为 JSON，与 PHP 版字段一致：

| 字段 | 含义 |
| --- | --- |
| `iv` | 本次加密的随机 IV/nonce（base64） |
| `value` | 密文（base64） |
| `mac` | CBC 的 HMAC-SHA256（hex，输入为 base64(iv)+base64(value)）；GCM 为空 |
| `tag` | GCM 的认证 tag（base64）；CBC 为空 |

每次加密都重新生成 IV，同一明文多次加密的结果不同。

## 失败与错误

| 哨兵错误 | 场景 |
| --- | --- |
| `ErrUnsupportedCipher` | cipher 不在支持表内或密钥长度不对（构造时） |
| `ErrEncrypt` | 序列化失败、随机数或底层 cipher 错误 |
| `ErrPayload` | 载荷不是合法 base64/JSON、缺字段、IV 长度不对 |
| `ErrMAC` | CBC 的 MAC 不匹配（先验 MAC，通过后才解密） |
| `ErrTag` | GCM tag 缺失或长度不是 16 字节；CBC 载荷带 tag |
| `ErrDecrypt` | 密钥或 cipher 与加密时不一致、GCM 认证失败、明文不是合法 JSON |

用 `errors.Is` 判断。错误信息不包含密钥和明文。

## 注意点

1. 密钥通常放在环境变量或密钥管理服务里，不要提交到仓库；本包不提供读取
   密钥的方法，避免意外写入日志。构造时会复制密钥，调用方之后修改原切片
   不影响实例。
2. `Decrypt` 只在校验通过后解析 JSON；不要用它解密来源不可信的载荷以外的
   场景做反序列化攻击面评估——JSON 本身不执行代码，但仍建议只解密本组件、
   同一密钥产出的载荷。
3. 更换密钥后旧密文无法解密，需要业务侧自行迁移。
4. GCM 的 nonce 是 12 字节随机数；同一密钥下随机 nonce 的安全上限约为
   2^32 次加密，超过这个量级应轮换密钥。

## 测试

```bash
go test ./support/encryption/
```

单元测试覆盖四种 cipher 的值/字符串往返、随机 IV、错误密钥、篡改
value/iv/mac/tag、非法载荷、密钥复制，以及 PHP 生成的固定密文互通。
固定密文由 swoolefy `Encrypter::encryptString` 生成（明文
`hello gofy 加密互通`，密钥 `0123456789abcdef` / 双份拼接），重新生成：

```bash
cd /path/to/library/src && php -r '
require "Exception/CommonException.php";
require "Encryption/Contracts/Encrypter.php";
require "Encryption/Contracts/StringEncrypter.php";
require "Encryption/Contracts/EncryptException.php";
require "Encryption/Contracts/DecryptException.php";
require "Encryption/Encrypter.php";
$e = new Swoolefy\Library\Encryption\Encrypter("0123456789abcdef", "aes-128-cbc");
echo $e->encryptString("hello gofy 加密互通"), "\n";
'
```

Go→PHP 方向已在开发期用相同方式验证：Go 加密的载荷由 PHP
`decryptString` 解出原文。
