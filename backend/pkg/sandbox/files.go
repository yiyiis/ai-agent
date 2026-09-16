package sandbox

// files.go 工作区文件传输的共享助手：docker（/archive tar 流）与 e2b（/files
// REST）共用同一套相对路径校验与单文件 tar 编解码。
import (
	"archive/tar"
	"bytes"
	"io"
	"path"
	"strings"
	"time"

	"backend/pkg/errors"
)

// cleanRel 校验并规范化工作区相对路径（斜杠分隔；拒绝绝对路径与 ../ 越界）
func cleanRel(rel string) (string, error) {
	r := path.Clean(strings.TrimSpace(rel))
	if r == "" || r == "." || path.IsAbs(r) || strings.HasPrefix(r, "../") {
		return "", errors.NewMsg("路径 " + rel + " 超出工作区范围，只能访问工作区内的相对路径")
	}
	return r, nil
}

// tarSingleFile 构造仅含一个文件条目的 tar 流（WriteFile 经 /archive 传入容器用；
// 不做 gzip——单文件量级下压缩收益小于 CPU 开销）
func tarSingleFile(rel string, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     path.Clean(rel),
		Mode:     0o644,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readTarSingle 解析单文件 tar 流，返回文件内容（ReadFile 经 /archive 拉取用）
func readTarSingle(tarBytes []byte) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.NewMsg("归档中没有文件条目")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // 跳过目录条目
		}
		return io.ReadAll(tr)
	}
}
