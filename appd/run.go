package appd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
)

const AppdStopped = -1

// Appd represents the appd binary
type Appd struct {
	pid            int
	path           string
	stdin          io.Reader
	stderr, stdout io.Writer
	cleanup        func()
}

// New takes a binary and untar it in a temporary directory.
func New(name string, bin []byte, cfg ...CfgOption) (*Appd, error) {
	if bin == nil {
		bin = CelestiaApp()
	}

	// untar the binary.
	gzr, err := gzip.NewReader(bytes.NewReader(bin))
	if err != nil {
		panic(err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	if _, err := tr.Next(); err != nil {
		return nil, err
	}

	binary, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}

	path, cleanup, err := saveBytesTemp(binary, fmt.Sprintf("celestia-app-%s", name), 0o755)
	if err != nil {
		return nil, err
	}

	appd := &Appd{
		path:    path,
		cleanup: cleanup,
	}

	for _, opt := range cfg {
		opt(appd)
	}

	return appd, nil
}

// saveBytesTemp saves data bytes to a temporary file location at path.
func saveBytesTemp(data []byte, prefix string, perm os.FileMode) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", prefix)
	if err != nil {
		return
	}
	defer f.Close()

	path = f.Name()
	cleanup = func() { os.Remove(path) }

	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	if _, err = f.Write(data); err != nil {
		return
	}

	err = os.Chmod(path, perm)

	return
}

func (a *Appd) Run(args ...string) error {
	cmd := exec.Command(a.path, args...)

	// Set up I/O
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", a.path, err)
	}

	a.pid = cmd.Process.Pid
	go func() {
		// wait for process to finish
		if err := cmd.Wait(); err != nil {
			// Log the error (consider using a proper logging package)
			fmt.Printf("Process finished with error: %v\n", err)
		}

		a.pid = -1 // reset pid
	}()

	return nil
}

func (a *Appd) Pid() int {
	return a.pid
}
