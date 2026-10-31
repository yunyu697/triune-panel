package backup

// 备份加密：用 age（filippo.io/age）的口令模式做流式加密。
//
// 为什么用 age 而不是自己拼 AES-GCM：
//   - 加密容器的坑几乎全在细节（分块 nonce 复用、截断攻击、KDF 参数、
//     头完整性），手写一遍就是给未来埋雷；
//   - age 是公开规范 + 独立实现，**没有面板也能解密**：
//     `age -d -o x.tar.gz x.tar.gz.age`，或者 `openssl` 之外的任何 age 工具。
//     面板二进制丢了、机器挂了，备份还救得回来，这对"最后一道保险"至关重要；
//   - 流式 API，不需要把整包先落盘再加密。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// EncryptedSuffix 加密归档的扩展名。
const EncryptedSuffix = ".age"

// MinPassphraseLen 口令最小长度（字符数）。
//
// age 的口令模式用 scrypt 派生密钥，弱口令是最现实的攻击面。
// 12 个字符是"能记住"和"扛得住离线爆破"之间的折中；真正的强随机口令
// 可以用 `triune-panel backup --gen-passphrase` 生成。
const MinPassphraseLen = 12

// ErrPassphraseRequired 归档已加密但调用方没提供口令。
// CLI 靠它决定"该提示用户输入口令"而不是直接报错退出。
var ErrPassphraseRequired = errors.New("归档已加密，需要口令")

// ageMagic age 文件格式的头部魔数（明文可见，用于识别）。
var ageMagic = []byte("age-encryption.org/v1")

// IsEncrypted 判断归档是否为 age 加密格式。
//
// 按**魔数**判断而不是扩展名：扩展名可以被随手改掉（`.tar.gz.age` 重命名成
// `.tar.gz` 很常见），魔数不会骗人。
func IsEncrypted(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return isEncryptedReader(f)
}

func isEncryptedReader(r io.Reader) (bool, error) {
	buf := make([]byte, len(ageMagic))
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	return bytes.HasPrefix(buf[:n], ageMagic), nil
}

// EncryptWriter 用口令把 w 包成加密写端。关闭返回的 writer 才会写出
// 最后一个分块与完整性校验，**必须 Close**，否则密文是截断的。
func EncryptWriter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	if len([]rune(passphrase)) < MinPassphraseLen {
		return nil, fmt.Errorf("口令太短：至少 %d 个字符", MinPassphraseLen)
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	return age.Encrypt(w, r)
}

// DecryptReader 用口令把 r 包成解密读端。
func DecryptReader(r io.Reader, passphrase string) (io.Reader, error) {
	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	dr, err := age.Decrypt(r, id)
	if err != nil {
		// age 在解析头部时就验证口令，所以口令错会在这里就报出来
		return nil, fmt.Errorf("解密失败（口令错误或归档已损坏）: %w", err)
	}
	return dr, nil
}

// openArchive 打开归档并返回"已解密"的读端。
//
// 明文归档直接返回文件句柄；加密归档需要口令，缺口令返回
// ErrPassphraseRequired（CLI 据此提示输入）。返回的 ReadCloser 关闭时
// 会连带关闭底层文件。
func openArchive(path, passphrase string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	enc, err := isEncryptedReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	if !enc {
		return f, nil
	}
	if passphrase == "" {
		f.Close()
		return nil, ErrPassphraseRequired
	}
	dr, err := DecryptReader(f, passphrase)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &wrappedReader{r: dr, c: f}, nil
}

type wrappedReader struct {
	r io.Reader
	c io.Closer
}

func (w *wrappedReader) Read(p []byte) (int, error) { return w.r.Read(p) }
func (w *wrappedReader) Close() error               { return w.c.Close() }
