package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/flanksource/commons-db/types"
	"github.com/hirochachacha/go-smb2"
)

// SMBSession holds SMB connection details
type SMBSession struct {
	net.Conn
	*smb2.Session
	*smb2.Share
}

func (s *SMBSession) Close() error {
	if s.Conn != nil {
		_ = s.Conn.Close()
	}
	if s.Session != nil {
		_ = s.Session.Logoff()
	}
	if s.Share != nil {
		_ = s.Share.Umount()
	}

	return nil
}

type smbFS struct {
	*SMBSession
}

type SMBFileInfo struct {
	Base string
	fs.FileInfo
}

func (t *SMBFileInfo) FullPath() string {
	return path.Join(t.Base, t.FileInfo.Name())
}

func NewSMBFS(server string, port, share string, auth types.Authentication) (*smbFS, error) {
	if port == "" {
		port = "445"
	}

	// Inline SMBConnect logic
	var err error
	var smb *SMBSession
	server = server + ":" + port
	conn, err := net.Dial("tcp", server)
	if err != nil {
		return nil, err
	}
	smb = &SMBSession{
		Conn: conn,
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     auth.GetUsername(),
			Password: auth.GetPassword(),
			Domain:   auth.GetDomain(),
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		return nil, err
	}
	smb.Session = s
	smbShare, err := s.Mount(share)
	if err != nil {
		return nil, err
	}

	smb.Share = smbShare

	return &smbFS{SMBSession: smb}, nil
}

func (s *smbFS) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	return s.SMBSession.Share.Open(path)
}

func (s *smbFS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	f, err := s.SMBSession.Share.Create(path)
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(f, data)
	if err != nil {
		return nil, fmt.Errorf("error writing file: %w", err)
	}

	return f.Stat()
}

func (t *smbFS) ReadDir(name string) ([]FileInfo, error) {
	if strings.Contains(name, "*") {
		return t.ReadDirGlob(name)
	}

	fileInfos, err := t.SMBSession.ReadDir(name)
	if err != nil {
		return nil, err
	}

	output := make([]FileInfo, 0, len(fileInfos))
	for _, fileInfo := range fileInfos {
		output = append(output, &SMBFileInfo{Base: name, FileInfo: fileInfo})
	}

	return output, nil
}

func (t *smbFS) ReadDirGlob(name string) ([]FileInfo, error) {
	base, pattern := doublestar.SplitPattern(name)
	matches, err := doublestar.Glob(t.DirFS(base), pattern)
	if err != nil {
		return nil, fmt.Errorf("error globbing pattern %q: %w", pattern, err)
	}

	output := make([]FileInfo, 0, len(matches))
	for _, match := range matches {
		fullPath := filepath.Join(base, match)
		info, err := t.Stat(fullPath)
		if err != nil {
			return nil, err
		}

		output = append(output, &SMBFileInfo{Base: name, FileInfo: info})
	}

	return output, nil
}
