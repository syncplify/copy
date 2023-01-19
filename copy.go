package copy

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/schollz/progressbar/v3"
)

type timespec struct {
	Mtime time.Time
	Atime time.Time
	Ctime time.Time
}

// Copy copies src to dest, doesn't matter if src is a directory or a file.
func Copy(src, dest string, opt ...Options) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	var p *progressbar.ProgressBar
	var initialBasePath string
	if len(opt) > 0 && opt[0].DirProgressBar && info.IsDir() {
		p = progressbar.Default(1, "Dir: "+filepath.Base(src))
		initialBasePath = filepath.Dir(src)
	}
	err = switchboard(src, dest, info, assureOptions(src, dest, opt...), p, initialBasePath)
	if p != nil {
		p.Describe("Dir: " + filepath.Base(src))
		p.Add(1)
		p.Finish()
	}
	return err
}

// switchboard switches proper copy functions regarding file type, etc...
// If there would be anything else here, add a case to this switchboard.
func switchboard(src, dest string, info os.FileInfo, opt Options, p *progressbar.ProgressBar, initialBasePath string) (err error) {

	if info.Mode()&os.ModeDevice != 0 && !opt.Specials {
		return err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		err = onsymlink(src, dest, opt)
	case info.IsDir():
		err = dcopy(src, dest, info, opt, p, initialBasePath)
	case info.Mode()&os.ModeNamedPipe != 0:
		err = pcopy(dest, info)
	default:
		err = fcopy(src, dest, info, opt)
	}

	return err
}

// copyNextOrSkip decide if this src should be copied or not.
// Because this "copy" could be called recursively,
// "info" MUST be given here, NOT nil.
func copyNextOrSkip(src, dest string, info os.FileInfo, opt Options, p *progressbar.ProgressBar, initialBasePath string) error {
	if opt.Skip != nil {
		skip, err := opt.Skip(info, src, dest)
		if err != nil {
			return err
		}
		if skip {
			return nil
		}
	}
	return switchboard(src, dest, info, opt, p, initialBasePath)
}

// fcopy is for just a file,
// with considering existence of parent directory
// and file permission.
func fcopy(src, dest string, info os.FileInfo, opt Options) (err error) {

	if err = os.MkdirAll(filepath.Dir(dest), os.ModePerm); err != nil {
		return
	}

	f, err := os.Create(dest)
	if err != nil {
		return
	}
	defer fclose(f, &err)

	chmodfunc, err := opt.PermissionControl(info, dest)
	if err != nil {
		return err
	}
	chmodfunc(&err)

	s, err := os.Open(src)
	if err != nil {
		return
	}
	defer fclose(s, &err)

	var buf []byte = nil
	var w io.Writer = f
	var r io.Reader = s

	if opt.WrapReader != nil {
		r = opt.WrapReader(s)
	} else if opt.FileProgressBar {
		r = WrapReaderPB(s)
	}

	if opt.CopyBufferSize != 0 {
		buf = make([]byte, opt.CopyBufferSize)
		// Disable using `ReadFrom` by io.CopyBuffer.
		// See https://github.com/otiai10/copy/pull/60#discussion_r627320811 for more details.
		w = struct{ io.Writer }{f}
		// r = struct{ io.Reader }{s}
	}

	if _, err = io.CopyBuffer(w, r, buf); err != nil {
		return err
	}

	if opt.Sync {
		err = f.Sync()
	}

	if opt.PreserveOwner {
		if err := preserveOwner(src, dest, info); err != nil {
			return err
		}
	}
	if opt.PreserveTimes {
		if err := preserveTimes(info, dest); err != nil {
			return err
		}
	}

	return
}

// dcopy is for a directory,
// with scanning contents inside the directory
// and pass everything to "copy" recursively.
func dcopy(srcdir, destdir string, info os.FileInfo, opt Options, p *progressbar.ProgressBar, initialBasePath string) (err error) {

	if skip, err := onDirExists(opt, srcdir, destdir); err != nil {
		return err
	} else if skip {
		return nil
	}

	// Make dest dir with 0755 so that everything writable.
	chmodfunc, err := opt.PermissionControl(info, destdir)
	if err != nil {
		return err
	}
	defer chmodfunc(&err)

	contents, err := ioutil.ReadDir(srcdir)
	if err != nil {
		return
	}

	if p != nil {
		cc := p.GetMax64()
		p.ChangeMax64(cc + int64(len(contents)))
		if info.IsDir() {
			newDesc, err := filepath.Rel(initialBasePath, srcdir)
			if err == nil {
				p.Describe("Dir: " + newDesc)
			}
		}
	}

	for _, content := range contents {
		cs, cd := filepath.Join(srcdir, content.Name()), filepath.Join(destdir, content.Name())

		if err = copyNextOrSkip(cs, cd, content, opt, p, initialBasePath); err != nil {
			// If any error, exit immediately
			return
		}
		if p != nil {
			p.Add(1)
		}
	}

	if opt.PreserveTimes {
		if err := preserveTimes(info, destdir); err != nil {
			return err
		}
	}

	if opt.PreserveOwner {
		if err := preserveOwner(srcdir, destdir, info); err != nil {
			return err
		}
	}

	return
}

func onDirExists(opt Options, srcdir, destdir string) (bool, error) {
	_, err := os.Stat(destdir)
	if err == nil && opt.OnDirExists != nil && destdir != opt.intent.dest {
		switch opt.OnDirExists(srcdir, destdir) {
		case Replace:
			if err := os.RemoveAll(destdir); err != nil {
				return false, err
			}
		case Untouchable:
			return true, nil
		} // case "Merge" is default behaviour. Go through.
	} else if err != nil && !os.IsNotExist(err) {
		return true, err // Unwelcome error type...!
	}
	return false, nil
}

func onsymlink(src, dest string, opt Options) error {
	switch opt.OnSymlink(src) {
	case Shallow:
		if err := lcopy(src, dest); err != nil {
			return err
		}
		if opt.PreserveTimes {
			return preserveLtimes(src, dest)
		}
		return nil
	case Deep:
		orig, err := os.Readlink(src)
		if err != nil {
			return err
		}
		info, err := os.Lstat(orig)
		if err != nil {
			return err
		}
		return copyNextOrSkip(orig, dest, info, opt, nil, "")
	case Skip:
		fallthrough
	default:
		return nil // do nothing
	}
}

// lcopy is for a symlink,
// with just creating a new symlink by replicating src symlink.
func lcopy(src, dest string) error {
	src, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(src, dest)
}

// fclose ANYHOW closes file,
// with asiging error raised during Close,
// BUT respecting the error already reported.
func fclose(f *os.File, reported *error) {
	if err := f.Close(); *reported == nil {
		*reported = err
	}
}
