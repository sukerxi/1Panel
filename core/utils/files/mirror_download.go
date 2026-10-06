package files

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/1Panel-dev/1Panel/core/constant"
)

// DownloadCandidate describes one download endpoint. Mirror endpoints are
// ghproxy-compatible prefixes, the last candidate is usually the direct URL.
type DownloadCandidate struct {
	Name string
	URL  string
}

// Mirror status values reported through MirrorStatusFunc.
const (
	MirrorProbing     = "probing"
	MirrorDownloading = "downloading"
	MirrorFailed      = "failed"
	MirrorSuccess     = "success"
)

type (
	// MirrorStatusFunc is called whenever a candidate changes state.
	MirrorStatusFunc func(name, status, detail string)
	// DownloadProgressFunc is called periodically while streaming.
	// total is 0 when the server does not expose the package size.
	DownloadProgressFunc func(downloaded, total, speedBps int64)
)

const (
	mirrorDialTimeout       = 8 * time.Second
	mirrorHeaderTimeout     = 15 * time.Second
	mirrorFirstByteTimeout  = 20 * time.Second
	mirrorStallTimeout      = 30 * time.Second
	mirrorProgressInterval  = time.Second
	mirrorIdleConnTimeout   = 15 * time.Second
	mirrorResponseChunkSize = 32 << 10
)

func newMirrorHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: (&net.Dialer{
			Timeout:   mirrorDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   mirrorDialTimeout,
		ResponseHeaderTimeout: mirrorHeaderTimeout,
		IdleConnTimeout:       mirrorIdleConnTimeout,
	}
	return &http.Client{Transport: transport}
}

func candidateHostName(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// CandidateName turns a mirror base URL into a short display name.
func CandidateName(rawURL string) string {
	return candidateHostName(rawURL)
}

type mirrorAttemptResult struct {
	statusCode int
	total      int64
	err        error
}

// parseContentRange extracts the complete length from a "bytes start-end/total" header.
func parseContentRange(contentRange string) int64 {
	if contentRange == "" {
		return 0
	}
	idx := strings.LastIndex(contentRange, "/")
	if idx < 0 {
		return 0
	}
	total, err := strconv.ParseInt(strings.TrimSpace(contentRange[idx+1:]), 10, 64)
	if err != nil || total <= 0 {
		return 0
	}
	return total
}

// streamMirror streams one candidate into dstPart. When the part file already
// exists, the download is resumed with a Range request. The returned result
// carries the total package size when the server announced it.
//
// Two timeouts guard a candidate so an unreachable upstream can never hang the
// whole upgrade: mirrorFirstByteTimeout while no body byte has arrived (many
// proxies answer headers instantly while their GitHub fetch stalls forever),
// and mirrorStallTimeout once data is flowing.
func streamMirror(ctx context.Context, client *http.Client, candidate DownloadCandidate, dstPart string,
	onMirror MirrorStatusFunc, onProgress DownloadProgressFunc) mirrorAttemptResult {
	existing, err := os.Stat(dstPart)
	var resumeFrom int64
	if err == nil {
		resumeFrom = existing.Size()
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, candidate.URL, nil)
	if err != nil {
		return mirrorAttemptResult{err: err}
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	resp, err := client.Do(req)
	if err != nil {
		return mirrorAttemptResult{err: fmt.Errorf("connect failed: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return mirrorAttemptResult{statusCode: resp.StatusCode, total: resumeFrom, err: errors.New("range not satisfiable")}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return mirrorAttemptResult{statusCode: resp.StatusCode, err: fmt.Errorf("unexpected http status: %d", resp.StatusCode)}
	}

	restart := resp.StatusCode == http.StatusOK
	if restart {
		resumeFrom = 0
		if err := os.Truncate(dstPart, 0); err != nil && !os.IsNotExist(err) {
			return mirrorAttemptResult{err: fmt.Errorf("truncate part file failed: %w", err)}
		}
	}

	var total int64
	if contentRange := resp.Header.Get("Content-Range"); contentRange != "" {
		total = parseContentRange(contentRange)
	}
	if total <= 0 && resp.ContentLength > 0 {
		total = resp.ContentLength + resumeFrom
	}

	flag := os.O_CREATE | os.O_WRONLY
	if restart {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	out, err := os.OpenFile(dstPart, flag, constant.FilePerm)
	if err != nil {
		return mirrorAttemptResult{err: fmt.Errorf("open part file failed: %w", err)}
	}

	if onMirror != nil {
		onMirror(candidate.Name, MirrorDownloading, "")
	}

	buf := make([]byte, mirrorResponseChunkSize)
	var (
		sessionBytes   atomic.Int64
		lastActiveNano atomic.Int64
		streamErr      error
		done           = make(chan struct{})
	)
	lastActiveNano.Store(time.Now().UnixNano())

	watchdog := time.NewTicker(mirrorProgressInterval)
	defer watchdog.Stop()

	go func() {
		defer close(done)
		if restart {
			// Reject interstitials/error pages served with a 200 status before
			// any byte reaches the part file. The gzip check runs inside this
			// goroutine on purpose: the watchdog must be able to abort a mirror
			// which answers headers but never streams a body.
			header := make([]byte, 2)
			n, readErr := io.ReadFull(resp.Body, header)
			lastActiveNano.Store(time.Now().UnixNano())
			if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				streamErr = fmt.Errorf("read package header failed: %w", readErr)
				return
			}
			if n != 2 || header[0] != 0x1f || header[1] != 0x8b {
				streamErr = errors.New("downloaded file is not a valid gzip package, the mirror may have returned an error page")
				return
			}
			if _, err := out.Write(header); err != nil {
				streamErr = fmt.Errorf("write part file failed: %w", err)
				return
			}
			sessionBytes.Store(2)
		}
		for {
			select {
			case <-reqCtx.Done():
				return
			default:
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := out.Write(buf[:n]); writeErr != nil {
					streamErr = fmt.Errorf("write part file failed: %w", writeErr)
					return
				}
				sessionBytes.Add(int64(n))
				lastActiveNano.Store(time.Now().UnixNano())
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					streamErr = fmt.Errorf("read response failed: %w", readErr)
				}
				return
			}
		}
	}()

	var (
		prevTickBytes = sessionBytes.Load()
		prevTickTime  = time.Now()
		windowSpeed   int64
	)
	for {
		select {
		case <-done:
			_ = out.Close()
			if streamErr != nil {
				return mirrorAttemptResult{statusCode: resp.StatusCode, total: total, err: streamErr}
			}
			if total > 0 {
				if info, statErr := os.Stat(dstPart); statErr == nil && info.Size() != total {
					return mirrorAttemptResult{
						statusCode: resp.StatusCode,
						total:      total,
						err:        fmt.Errorf("download incomplete: got %d bytes, expected %d", info.Size(), total),
					}
				}
			}
			return mirrorAttemptResult{statusCode: resp.StatusCode, total: total}
		case now := <-watchdog.C:
			idle := time.Since(time.Unix(0, lastActiveNano.Load()))
			if sessionBytes.Load() == 0 {
				if idle > mirrorFirstByteTimeout {
					cancel()
					_ = out.Close()
					return mirrorAttemptResult{
						statusCode: resp.StatusCode,
						total:      total,
						err:        fmt.Errorf("no data received within %s", mirrorFirstByteTimeout),
					}
				}
			} else if idle > mirrorStallTimeout {
				cancel()
				_ = out.Close()
				return mirrorAttemptResult{
					statusCode: resp.StatusCode,
					total:      total,
					err:        fmt.Errorf("download stalled for more than %s", mirrorStallTimeout),
				}
			}
			if onProgress != nil {
				cur := sessionBytes.Load()
				elapsed := now.Sub(prevTickTime).Nanoseconds()
				if elapsed > 0 {
					instant := (cur - prevTickBytes) * int64(time.Second) / elapsed
					if windowSpeed == 0 {
						windowSpeed = instant
					} else {
						windowSpeed = windowSpeed*7/10 + instant*3/10
					}
				}
				prevTickBytes = cur
				prevTickTime = now
				onProgress(resumeFrom+cur, total, windowSpeed)
			}
		case <-reqCtx.Done():
			_ = out.Close()
			return mirrorAttemptResult{err: reqCtx.Err()}
		}
	}
}

// DownloadFileWithMirrors downloads the package trying each candidate in order.
// A reachable candidate is streamed to dst+".part"; if the stream fails midway
// the next candidate resumes from the part file. The completed file is renamed
// to dst atomically.
func DownloadFileWithMirrors(candidates []DownloadCandidate, dst string,
	onMirror MirrorStatusFunc, onProgress DownloadProgressFunc) error {
	if len(candidates) == 0 {
		return errors.New("no download candidate provided")
	}
	partPath := dst + ".part"
	client := newMirrorHTTPClient()
	defer client.CloseIdleConnections()

	var lastErr error
	for _, candidate := range candidates {
		if candidate.Name == "" {
			candidate.Name = candidateHostName(candidate.URL)
		}
		if onMirror != nil {
			onMirror(candidate.Name, MirrorProbing, "")
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := streamMirror(ctx, client, candidate, partPath, onMirror, onProgress)
		if result.err == nil {
			cancel()
			if onMirror != nil {
				onMirror(candidate.Name, MirrorSuccess, "")
			}
			if onProgress != nil && result.total > 0 {
				onProgress(result.total, result.total, 0)
			}
			_ = os.Remove(dst)
			if err := os.Rename(partPath, dst); err != nil {
				return fmt.Errorf("rename downloaded file [%s] failed: %w", dst, err)
			}
			return nil
		}
		cancel()
		lastErr = fmt.Errorf("%s: %w", candidate.Name, result.err)
		if onMirror != nil {
			onMirror(candidate.Name, MirrorFailed, result.err.Error())
		}
	}
	_ = os.Remove(partPath)
	return fmt.Errorf("all download sources failed, last error: %w", lastErr)
}
