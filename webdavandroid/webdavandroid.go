package webdavandroid

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-webdav"
)

// AndroidFileSystem implements the webdav.FileSystem interface for Android's local file system.
type AndroidFileSystem struct {
	basePath string
}

// NewAndroidFileSystem creates a new AndroidFileSystem instance.
func NewAndroidFileSystem(basePath string) *AndroidFileSystem {
	return &AndroidFileSystem{basePath: basePath}
}

func (fs *AndroidFileSystem) resolvePath(name string) string {
	return filepath.Join(fs.basePath, filepath.Clean(string(os.PathSeparator)+name))
}

func (fs *AndroidFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	path := fs.resolvePath(name)
	return os.Open(path)
}

func (fs *AndroidFileSystem) Stat(ctx context.Context, name string) (*webdav.FileInfo, error) {
	path := fs.resolvePath(name)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return toWebDAVFileInfo(fs.basePath, path, info), nil
}

func (fs *AndroidFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]webdav.FileInfo, error) {
	path := fs.resolvePath(name)
	var files []webdav.FileInfo

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}

		fullPath := filepath.Join(path, entry.Name())

		// Only add direct children if not recursive
		if !recursive && strings.Contains(filepath.Clean(strings.TrimPrefix(fullPath, path)), string(os.PathSeparator)) {
			continue
		}

		files = append(files, *toWebDAVFileInfo(fs.basePath, fullPath, info))

		if recursive && entry.IsDir() {
			subFiles, err := fs.ReadDir(ctx, filepath.Join(name, entry.Name()), true)
			if err != nil {
				return nil, err
			}
			files = append(files, subFiles...)
		}
	}

	return files, nil
}

func (fs *AndroidFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *webdav.CreateOptions) (fileInfo *webdav.FileInfo, created bool, err error) {
	path := fs.resolvePath(name)
	flags := os.O_RDWR | os.O_CREATE | os.O_TRUNC
	f, err := os.OpenFile(path, flags, 0666)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	_, err = io.Copy(f, body)
	if err != nil {
		return nil, false, err
	}

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}

	return toWebDAVFileInfo(fs.basePath, path, info), true, nil // Always created or truncated
}

func (fs *AndroidFileSystem) RemoveAll(ctx context.Context, name string, opts *webdav.RemoveAllOptions) error {
	path := fs.resolvePath(name)
	return os.RemoveAll(path)
}

func (fs *AndroidFileSystem) Mkdir(ctx context.Context, name string) error {
	path := fs.resolvePath(name)
	return os.MkdirAll(path, 0755)
}

func (fs *AndroidFileSystem) Copy(ctx context.Context, name, dest string, options *webdav.CopyOptions) (created bool, err error) {
	srcPath := fs.resolvePath(name)
	destPath := fs.resolvePath(dest)

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false, err
	}

	if srcInfo.IsDir() {
		return false, &os.PathError{Op: "copy", Path: srcPath, Err: os.ErrInvalid} // Not directly supported for directories
	}

	if _, err := os.Stat(destPath); err == nil && options.NoOverwrite {
		return false, os.ErrExist
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()

	destFile, err := os.Create(destPath)
	if err != nil {
		return false, err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (fs *AndroidFileSystem) Move(ctx context.Context, name, dest string, options *webdav.MoveOptions) (created bool, err error) {
	srcPath := fs.resolvePath(name)
	destPath := fs.resolvePath(dest)

	if _, err := os.Stat(destPath); err == nil && options.NoOverwrite {
		return false, os.ErrExist
	}

	err = os.Rename(srcPath, destPath)
	if err != nil {
		return false, err
	}
	return true, nil
}

func toWebDAVFileInfo(basePath, path string, info os.FileInfo) *webdav.FileInfo {
	// Make the path relative to the base path for WebDAV clients.
	relPath, err := filepath.Rel(basePath, path)
	if err != nil {
		// If path is not within basePath, return base name or an error.
		// For simplicity, we'll return the base name.
		relPath = filepath.Base(path)
	}
	return &webdav.FileInfo{
		Path:    filepath.ToSlash(relPath),
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		// MIMEType and ETag are not easily determined from os.FileInfo
	}
}

// WebDAVServer is the main entry point for the Android application.
type WebDAVServer struct {
	handler *webdav.Handler
}

// NewWebDAVServer creates a new WebDAVServer instance.
func NewWebDAVServer(basePath string) *WebDAVServer {
	fs := NewAndroidFileSystem(basePath)
	handler := &webdav.Handler{
		FileSystem: fs,
	}
	return &WebDAVServer{handler: handler}
}

// HTTPResponse represents an HTTP response to be sent back to Android.
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// HandleHTTPRequest handles an incoming HTTP request and returns an HTTPResponse.
func (s *WebDAVServer) HandleHTTPRequest(method, urlPath string, headers map[string]string, body []byte) *HTTPResponse {
	reqBody := bytes.NewReader(body)
	req, err := http.NewRequest(method, urlPath, reqBody)
	if err != nil {
		return &HTTPResponse{StatusCode: http.StatusInternalServerError, Body: []byte(err.Error())}
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Create a custom ResponseWriter to capture the response
	recorder := newResponseRecorder()
	s.handler.ServeHTTP(recorder, req)

	respHeaders := make(map[string]string)
	for k, v := range recorder.Header() {
		respHeaders[k] = strings.Join(v, ", ")
	}

	return &HTTPResponse{
		StatusCode: recorder.statusCode,
		Headers:    respHeaders,
		Body:       recorder.body.Bytes(),
	}
}

// GetCurrentTime returns the current Unix timestamp. This function is added to ensure
// the 'time' package is always imported and not removed by the formatter.
func GetCurrentTime() int64 {
	return time.Now().Unix()
}

// responseRecorder is a simple http.ResponseWriter implementation to capture response.
type responseRecorder struct {
	headers    http.Header
	body       *bytes.Buffer
	statusCode int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{
		headers:    make(http.Header),
		body:       new(bytes.Buffer),
		statusCode: http.StatusOK, // Default status code
	}
}

func (r *responseRecorder) Header() http.Header {
	return r.headers
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}
