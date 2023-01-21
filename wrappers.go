package copy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/schollz/progressbar/v3"
)

type progressReader struct {
	src  *os.File
	pbar *progressbar.ProgressBar
}

func WrapReaderPB(src *os.File, name string) io.Reader {
	finfo, err := src.Stat()
	if err != nil {
		return src
	}
	return &progressReader{
		src:  src,
		pbar: progressbar.DefaultBytes(finfo.Size(), fmt.Sprintf("File: %s", filepath.Base(name))),
	}
}

func (p *progressReader) Read(b []byte) (n int, err error) {
	n, err = p.src.Read(b)
	p.pbar.Add(n)
	return
}
