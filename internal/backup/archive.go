package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// writeEncryptedPackage 把 tar.gz 内容以 age 口令加密写入目标文件，
// 同时返回加密后文件的 SHA-256。目标文件已存在时失败。
func writeEncryptedPackage(target, passphrase string, fill func(*tar.Writer) error) (string, int64, error) {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("创建备份包: %w", err)
	}
	hasher := sha256.New()
	written := &countingWriter{writer: io.MultiWriter(file, hasher)}

	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		file.Close()
		return "", 0, fmt.Errorf("创建备份加密口令: %w", err)
	}
	recipient.SetWorkFactor(16)

	ageWriter, err := age.Encrypt(written, recipient)
	if err != nil {
		file.Close()
		return "", 0, fmt.Errorf("初始化备份加密: %w", err)
	}
	gzipWriter := gzip.NewWriter(ageWriter)
	tarWriter := tar.NewWriter(gzipWriter)

	if err := fill(tarWriter); err != nil {
		tarWriter.Close()
		gzipWriter.Close()
		ageWriter.Close()
		file.Close()
		return "", 0, err
	}
	if err := tarWriter.Close(); err != nil {
		gzipWriter.Close()
		ageWriter.Close()
		file.Close()
		return "", 0, fmt.Errorf("结束备份归档: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		ageWriter.Close()
		file.Close()
		return "", 0, fmt.Errorf("压缩备份: %w", err)
	}
	if err := ageWriter.Close(); err != nil {
		file.Close()
		return "", 0, fmt.Errorf("完成备份加密: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", 0, fmt.Errorf("同步备份包: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("关闭备份包: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), written.count, nil
}

// addFile 把磁盘文件写入 tar。
func addFile(writer *tar.Writer, name, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("读取备份内容 %s: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取备份内容信息 %s: %w", name, err)
	}
	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("写入备份头 %s: %w", name, err)
	}
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("写入备份内容 %s: %w", name, err)
	}
	return nil
}

func addBytes(writer *tar.Writer, name string, data []byte) error {
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("写入备份头 %s: %w", name, err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("写入备份内容 %s: %w", name, err)
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	c.count += int64(n)
	return n, err
}
