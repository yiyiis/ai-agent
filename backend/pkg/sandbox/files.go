package sandbox

// files.go 工作区文件传输的共享助手：docker（/archive tar 流）与 e2b（/files
// REST）共用同一套相对路径校验与 tar 编解码；tarWorkspace/untarTo 同时服务
// 两驱动的"宿主镜像 ↔ 沙箱"整包同步。
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path"
	"path/filepath"
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

// isSyncSkipped 整包同步时跳过的文件：marker 与同步用 tar 包。
// .pyexec_* 临时脚本不能排除——脚本必须进沙箱才能执行（由工具层执行后清理）。
func isSyncSkipped(rel string) bool {
	base := filepath.Base(rel)
	return base == e2bSyncMarker || base == ".sync.tgz" || base == "sbx_out.tgz"
}

// tarWorkspace 把宿主工作区打包为 tar.gz 字节流
func tarWorkspace(root string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	err := filepath.WalkDir(root, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || isSyncSkipped(filepath.ToSlash(rel)) {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		return copyErr
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// untarTo 把 tar.gz 解包到目录（覆盖已存在文件，跳过同步排除项）
func untarTo(dir string, data []byte) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if isSyncSkipped(filepath.ToSlash(name)) {
			continue
		}
		// 防路径穿越：解析后的目标必须落在 dir 内
		target := filepath.Join(dir, filepath.FromSlash(name))
		rel, err := filepath.Rel(dir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeTarFile(target, tr); err != nil {
				return err
			}
		}
	}
}

func writeTarFile(target string, r io.Reader) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
