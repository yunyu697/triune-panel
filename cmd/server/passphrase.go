package main

// 备份口令的读取与生成。
//
// 口令**不接受命令行明文参数**（`--passphrase xxx` 会留在 shell 历史、
// 进程列表 /proc/<pid>/cmdline 里，同机其他用户可直接读到）。
// 只支持四条路径，按优先级：
//   1. --passphrase-file <文件>   适合 systemd timer 等无人值守场景
//   2. --passphrase-stdin         适合 `echo | ...` / 管道 / 密钥管理工具
//   3. 环境变量 TRIUNE_BACKUP_PASSPHRASE
//   4. 交互式无回显输入（终端里才会走到这里）

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"triune/internal/backup"
)

// envPassphraseEnv 环境变量名。
const envPassphraseEnv = "TRIUNE_BACKUP_PASSPHRASE"

// passphraseOpts 命令行里与口令相关的开关。
type passphraseOpts struct {
	file  *string
	stdin *bool
	gen   *bool // 仅 backup 用：生成随机口令
}

// resolvePassphrase 按优先级取口令；返回空串表示"用户没要求加密"。
//
// encryptHint 为真（用户显式加了 --encrypt）时，前三个来源都没有就走交互输入。
func resolvePassphrase(o passphraseOpts, encryptHint bool) (string, error) {
	if o.file != nil && *o.file != "" {
		b, err := os.ReadFile(*o.file)
		if err != nil {
			return "", fmt.Errorf("读取口令文件失败: %w", err)
		}
		p := strings.TrimRight(string(b), "\r\n")
		if p == "" {
			return "", fmt.Errorf("口令文件 %s 是空的", *o.file)
		}
		return p, nil
	}
	if o.stdin != nil && *o.stdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("从标准输入读取口令失败: %w", err)
		}
		p := strings.TrimRight(line, "\r\n")
		if p == "" {
			return "", errors.New("标准输入里没有口令")
		}
		return p, nil
	}
	if p := strings.TrimSpace(os.Getenv(envPassphraseEnv)); p != "" {
		return p, nil
	}
	if encryptHint {
		return promptPassphrase("设置备份口令: ", true)
	}
	return "", nil
}

// promptPassphrase 交互式读取口令（无回显）。confirm 为真时要求输入两次。
// 非终端（管道/重定向）下退化为按行读取，避免 `--encrypt` 在脚本里直接卡死；
// 此时若 stdin 只给了一行（EOF），确认步骤自动跳过——脚本里请优先用
// --passphrase-stdin / --passphrase-file。
func promptPassphrase(prompt string, confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// 两次读取必须共用同一个 Reader：bufio 会预读，各自新建会把
		// 第二行吞进第一个 Reader 的缓冲区里。
		r := bufio.NewReader(os.Stdin)
		p, err := readLinePrompt(r, prompt)
		if err != nil {
			return "", err
		}
		if len([]rune(p)) < backup.MinPassphraseLen {
			return "", fmt.Errorf("口令太短：至少 %d 个字符", backup.MinPassphraseLen)
		}
		if confirm {
			again, err := readLinePrompt(r, "再输一次确认: ")
			if err == nil && again != p {
				return "", errors.New("两次输入的口令不一致")
			}
		}
		return p, nil
	}

	fmt.Fprint(os.Stderr, prompt)
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	p := string(first)
	if len([]rune(p)) < backup.MinPassphraseLen {
		return "", fmt.Errorf("口令太短：至少 %d 个字符", backup.MinPassphraseLen)
	}
	if !confirm {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "再输一次确认: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	if string(second) != p {
		return "", errors.New("两次输入的口令不一致")
	}
	return p, nil
}

// readLinePrompt 非终端下打印提示并读一行（去掉行尾换行）。
func readLinePrompt(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	fmt.Fprintln(os.Stderr) // 回显的输入不会被终端打印，补个换行
	p := strings.TrimRight(line, "\r\n")
	if p == "" {
		return "", errors.New("口令为空")
	}
	return p, nil
}

// genPassphrase 生成随机口令。
//
// 用 32 字符表（去掉 0/O/1/l/I 等易混字符）取 24 位 ≈ 120 bit 熵，
// 足够扛住对 age 头部的离线爆破；分组显示只是为了便于抄写。
func genPassphrase(groups int) (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	const perGroup = 4
	buf := make([]byte, groups*perGroup)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, c := range buf {
		if i > 0 && i%perGroup == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return b.String(), nil
}

// requirePassphraseForArchive 恢复前调用：归档加密了就必须拿到口令。
// 明文归档返回空串（不提示、不打扰）。
func requirePassphraseForArchive(archive string, o passphraseOpts) (string, error) {
	enc, err := backup.IsEncrypted(archive)
	if err != nil {
		return "", err
	}
	if !enc {
		return "", nil
	}
	p, err := resolvePassphrase(o, false)
	if err != nil {
		return "", err
	}
	if p == "" {
		p, err = promptPassphrase("该归档已加密，请输入口令: ", false)
		if err != nil {
			return "", fmt.Errorf("%w（也可用 --passphrase-file / %s 提供）", err, envPassphraseEnv)
		}
	}
	return p, nil
}
